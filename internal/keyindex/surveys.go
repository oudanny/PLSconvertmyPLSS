package keyindex

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"sort"
	"strings"
)

// SurveyPart expresses an exact survey identifier as an independent string
// (Base0), or as a suffix of township/first/second ID (Base1/2/3). An empty
// suffix preserves equality with a parent; dictionary zero alone means absent.
type SurveyPart struct {
	Text string `json:"text,omitempty"`
	Base uint8  `json:"base,omitempty"`
}

func (b *Builder) factorSurveys(m *manifest) []byte {
	m.Surveys = nil
	m.Exceptions = make(map[string][]uint32)
	for key, ids := range b.exceptions {
		if !strings.HasPrefix(key, "4:") {
			m.Exceptions[key] = ids
		}
	}
	m.SurveyDict = []SurveyPart{{}}
	dictionary := map[SurveyPart]uint32{{}: 0}
	codes := make([]uint32, len(b.rows))
	for i, row := range b.rows {
		survey := b.surveys[uint32(i+1)]
		if survey == "" {
			continue
		}
		var parents [3]string
		for field, code := range row.values {
			p := b.dicts[field][code]
			parents[field] = p.Text
			if p.Append && field > 0 {
				parents[field] = parents[field-1] + p.Text
			}
		}
		part := SurveyPart{Text: survey}
		for field := 2; field >= 0; field-- {
			if parents[field] != "" && strings.HasPrefix(survey, parents[field]) {
				part = SurveyPart{Text: survey[len(parents[field]):], Base: uint8(field + 1)}
				break
			}
		}
		code, ok := dictionary[part]
		if !ok {
			code = uint32(len(m.SurveyDict))
			dictionary[part] = code
			m.SurveyDict = append(m.SurveyDict, part)
		}
		codes[i] = code
		m.SurveyRows++
		if parents[0] == "" || !strings.HasPrefix(survey, parents[0]) {
			key := "4:" + survey
			m.Exceptions[key] = append(m.Exceptions[key], uint32(i+1))
		}
	}
	m.SurveyBits = uint8(bits.Len32(uint32(len(m.SurveyDict) - 1)))
	packedBytes := (uint64(len(codes))*uint64(m.SurveyBits) + 7) / 8
	width := (int(m.SurveyBits) + 7) / 8
	sparseBytes := uint64(m.SurveyRows) * uint64(4+width)
	if sparseBytes < packedBytes {
		m.SurveyMode = "sparse"
		out := make([]byte, 0, int(sparseBytes))
		for i, code := range codes {
			if code != 0 {
				out = binary.LittleEndian.AppendUint32(out, uint32(i+1))
				for j := 0; j < width; j++ {
					out = append(out, byte(code>>uint(8*j)))
				}
			}
		}
		return out
	}
	m.SurveyMode = "packed"
	out := make([]byte, int(packedBytes))
	for i, code := range codes {
		bit := uint64(i) * uint64(m.SurveyBits)
		for left := int(m.SurveyBits); left > 0; {
			shift := uint(bit % 8)
			n := min(left, 8-int(shift))
			out[bit/8] |= byte(code&uint32((1<<n)-1)) << shift
			code >>= uint(n)
			left -= n
			bit += uint64(n)
		}
	}
	return out
}

func (x *Index) surveyDataSize() (uint64, error) {
	m := &x.m
	if len(m.Surveys) != 0 || len(m.SurveyDict) == 0 || m.SurveyDict[0] != (SurveyPart{}) || m.SurveyBits > 32 || m.SurveyBits != uint8(bits.Len32(uint32(len(m.SurveyDict)-1))) || m.SurveyRows > m.Count {
		return 0, errors.New("invalid survey dictionary or counts")
	}
	seen := make(map[SurveyPart]bool, len(m.SurveyDict))
	for i, p := range m.SurveyDict {
		if p.Base > 3 || (i > 0 && p == (SurveyPart{})) || seen[p] {
			return 0, errors.New("invalid or duplicate survey dictionary entry")
		}
		seen[p] = true
	}
	switch m.SurveyMode {
	case "packed":
		return (uint64(m.Count)*uint64(m.SurveyBits) + 7) / 8, nil
	case "sparse":
		return uint64(m.SurveyRows) * uint64(4+(int(m.SurveyBits)+7)/8), nil
	default:
		return 0, errors.New("invalid survey code encoding")
	}
}

func (x *Index) surveyCode(id uint32) (uint32, error) {
	if id == 0 || id > x.m.Count {
		return 0, errors.New("survey row outside key index")
	}
	var code uint32
	if x.m.SurveyMode == "sparse" {
		width := (int(x.m.SurveyBits) + 7) / 8
		stride := 4 + width
		i := sort.Search(int(x.m.SurveyRows), func(i int) bool { return binary.LittleEndian.Uint32(x.surveyData[i*stride:]) >= id })
		if i == int(x.m.SurveyRows) || binary.LittleEndian.Uint32(x.surveyData[i*stride:]) != id {
			return 0, nil
		}
		for j := 0; j < width; j++ {
			code |= uint32(x.surveyData[i*stride+4+j]) << uint(8*j)
		}
	} else {
		bit := uint64(id-1) * uint64(x.m.SurveyBits)
		shift := uint(0)
		for left := int(x.m.SurveyBits); left > 0; {
			offset := uint(bit % 8)
			n := min(left, 8-int(offset))
			code |= uint32((x.surveyData[bit/8]>>offset)&byte((1<<n)-1)) << shift
			left -= n
			shift += uint(n)
			bit += uint64(n)
		}
	}
	if code >= uint32(len(x.m.SurveyDict)) {
		return 0, errors.New("survey dictionary reference out of range")
	}
	return code, nil
}

func (x *Index) surveyValue(id uint32, c [3]uint32) (string, error) {
	if !x.version3 {
		return x.m.Surveys[id], nil
	}
	code, err := x.surveyCode(id)
	if err != nil {
		return "", err
	}
	p := x.m.SurveyDict[code]
	if p.Base == 0 {
		return p.Text, nil
	}
	return x.value(c, int(p.Base)-1) + p.Text, nil
}

func (x *Index) matchField(row uint32, c [3]uint32, field int, id string) (bool, error) {
	if field < 3 {
		return x.match(c, field, id), nil
	}
	if !x.version3 {
		return x.m.Surveys[row] == id, nil
	}
	code, err := x.surveyCode(row)
	if err != nil {
		return false, err
	}
	p := x.m.SurveyDict[code]
	if p.Base == 0 {
		return p.Text == id, nil
	}
	if !strings.HasSuffix(id, p.Text) {
		return false, nil
	}
	return x.match(c, int(p.Base)-1, id[:len(id)-len(p.Text)]), nil
}

func (x *Index) validateSurveyCodes() error {
	if !x.version3 {
		return nil
	}
	size, err := x.surveyDataSize()
	if err != nil {
		return err
	}
	if size != uint64(len(x.surveyData)) {
		return errors.New("survey code payload size disagrees with metadata")
	}
	if x.m.SurveyMode == "sparse" {
		stride := 4 + (int(x.m.SurveyBits)+7)/8
		previous := uint32(0)
		for i := 0; i < int(x.m.SurveyRows); i++ {
			id := binary.LittleEndian.Uint32(x.surveyData[i*stride:])
			if id <= previous || id > x.m.Count {
				return errors.New("invalid sparse survey row order/range")
			}
			var code uint32
			for j := 0; j < stride-4; j++ {
				code |= uint32(x.surveyData[i*stride+4+j]) << uint(8*j)
			}
			if code == 0 || code >= uint32(len(x.m.SurveyDict)) {
				return errors.New("invalid sparse survey code")
			}
			previous = id
		}
	} else if trailing := uint64(x.m.Count) * uint64(x.m.SurveyBits) % 8; trailing != 0 {
		if x.surveyData[len(x.surveyData)-1]>>uint(trailing) != 0 {
			return errors.New("nonzero survey padding bits")
		}
	}
	return nil
}
