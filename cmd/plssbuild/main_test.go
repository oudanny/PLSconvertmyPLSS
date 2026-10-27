package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func makeSource(t *testing.T, path, state string) {
	t.Helper()
	w, err := plss.CreatePack(path, plss.PackMetadata{State: state})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	g := plss.Geometry{Rings: [][][]float64{{{-120, 40}, {-119, 40}, {-119, 41}, {-120, 41}, {-120, 40}}}}
	if err = w.Add(3, map[string]any{"STATEABBR": state, "PLSSID": state + "town", "FRSTDIVID": state + "townsection", "SECDIVID": state + "townsectiondivision"}, g); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestWesternBuildResumeAndVerifyWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	for _, state := range plss.SupportedDownloadStates() {
		makeSource(t, filepath.Join(dir, strings.ToLower(state)+".sqlite"), state)
	}
	out := filepath.Join(dir, "west.atlas")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"west", "--work-dir", dir, "--out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("%v\n%s", err, &stderr)
	}
	r, err := regionatlas.OpenMapped(out)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := r.Lookup(context.Background(), 40.5, -119.5)
	if err != nil || len(hits) != 11 {
		t.Fatalf("%v %v", hits, err)
	}
	r.Close()
	if err = run([]string{"verify", "--data", out}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err = run([]string{"west", "--work-dir", dir, "--out", out}, &stdout, &stderr); err == nil {
		t.Fatal("overwrote existing region")
	}
	second := filepath.Join(dir, "resumed.atlas")
	stderr.Reset()
	if err = run([]string{"west", "--work-dir", dir, "--out", second}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stderr.String(), "reused completed atlas") != 11 {
		t.Fatal(stderr.String())
	}
	// A changed source must not silently reuse an old compact file.
	f, err := os.OpenFile(filepath.Join(dir, "az.sqlite"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = run([]string{"west", "--work-dir", dir, "--out", filepath.Join(dir, "different.atlas")}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "different source") {
		t.Fatalf("got %v", err)
	}
}

func TestBuilderRejectsInvalidFlagsBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"state", "--db", "x", "--out", "y", "--vertices", "rounded"},
		{"west", "--out", "x", "--source", "TX=some.sqlite"},
		{"west", "--out", "x", "--source", "WA=a", "--source", "WA=b"},
		{"west", "--out", "x", "--workers", "9"},
		{"bundle", "--out", "x"},
	} {
		var output bytes.Buffer
		if err := run(args, &output, &output); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
