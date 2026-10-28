"use strict";
(() => {
  const $ = id => document.getElementById(id);
  const stateNames = {AK:"Alaska",AL:"Alabama",AR:"Arkansas",AZ:"Arizona",CA:"California",CO:"Colorado",FL:"Florida",IA:"Iowa",ID:"Idaho",IL:"Illinois",IN:"Indiana",KS:"Kansas",LA:"Louisiana",MI:"Michigan",MN:"Minnesota",MO:"Missouri",MS:"Mississippi",MT:"Montana",ND:"North Dakota",NE:"Nebraska",NM:"New Mexico",NV:"Nevada",OH:"Ohio",OK:"Oklahoma",OR:"Oregon",SD:"South Dakota",TN:"Tennessee",TX:"Texas",UT:"Utah",WA:"Washington",WI:"Wisconsin",WY:"Wyoming",ZZ:"Unassigned source records"};
  const examples = {reno:[39.5296,-119.8138],boulder:[40.015,-105.2705],anchorage:[61.2181,-149.9003]};
  const inputModes = {
    latlon:{label:"Latitude, longitude",help:"Decimal degrees, DMS, DDM, or ISO 6709.",placeholder:"39.5296, -119.8138",value:"39.5296, -119.8138"},
    utm:{label:"UTM coordinate",help:"Zone + hemisphere, easting, northing. N / S mean hemispheres.",placeholder:"11N 258156.249 4379330.926",value:"11N 258156.249 4379330.926"},
    plss:{label:"Published BLM identifier",help:"Use an exact published ID. An area returns an interior representative point.",placeholder:"NV210190N0190E0SN110ASWNE",value:"NV210190N0190E0SN110ASWNE"}
  };
  let kind = "latlon", engineReady = false, atlas = null, busy = false, manualAtlasChoice = false;
  let lastResult = null, selectedIndex = 0, requestSequence = 0, pendingCount = 0;
  let polygonLayers = [], pointMarker = null, toastTimer = null;
  const pending = new Map();
  const worker = new Worker("worker.js");
  const map = L.map("map", {zoomControl:false, minZoom:2, maxZoom:20, preferCanvas:true, worldCopyJump:false, zoomSnap:0.25});
  map.setView([39.7,-99.6],4);
  L.control.zoom({position:"bottomright"}).addTo(map);
  L.control.scale({position:"bottomleft",imperial:true,metric:true,maxWidth:110}).addTo(map);
  map.attributionControl.setPrefix(false);
  map.attributionControl.addAttribution("Natural Earth · BLM · Leaflet");
  map.createPane("survey"); map.getPane("survey").style.zIndex = 430;
  map.createPane("cityLabels"); map.getPane("cityLabels").style.zIndex = 410;
  map.getPane("cityLabels").style.pointerEvents = "none";
  const geography = L.layerGroup().addTo(map);
  const labels = L.layerGroup().addTo(map);
  let geographyData = null;
  let cityData = [];

  function element(tag, className, text) {
    const item = document.createElement(tag);
    if (className) item.className = className;
    if (text != null) item.textContent = text;
    return item;
  }
  function bytes(value) {
    if (!Number.isFinite(value)) return "—";
    return value >= 1e9 ? `${(value/1e9).toFixed(2)} GB` : `${(value/1e6).toFixed(1)} MB`;
  }
  function duration(ms) { return ms < 1 ? `${Math.round(ms*1000)} µs` : ms < 1000 ? `${ms.toFixed(ms<10?1:0)} ms` : `${(ms/1000).toFixed(1)} s`; }
  function message(text, error = false) {
    $("message").textContent = text || "";
    $("message").classList.toggle("hidden", !text);
    $("message").classList.toggle("error", error);
  }
  function toast(text, persistent = false) {
    clearTimeout(toastTimer);
    $("map-toast").textContent = text;
    $("map-toast").classList.toggle("hidden", !text);
    if (!persistent) toastTimer = setTimeout(() => $("map-toast").classList.add("hidden"), 2500);
  }
  function updateBusy() {
    busy = pendingCount > 0;
    $("search-button").disabled = !engineReady || busy;
    $("open-atlas").disabled = !engineReady || busy;
    $("query").disabled = busy;
    $("id-kind").disabled = busy;
    for(const button of document.querySelectorAll("[role=tab],[data-example]"))button.disabled=busy;
    $("search-button").querySelector("span").textContent = busy ? "Reading your atlas…" : "Explore this location";
    $("search-form").setAttribute("aria-busy", String(busy));
  }
  function rpc(command, payload = {}, file) {
    return new Promise((resolve, reject) => {
      const id = ++requestSequence;
      pending.set(id, {resolve, reject});
      pendingCount++;
      updateBusy();
      worker.postMessage({id,command,payload,file});
    });
  }
  function statsText(stats) {
    if (!stats) return "Go WebAssembly · local processing";
    return `Go WebAssembly · ${bytes(stats.bytes_read || 0)} read · ${bytes(stats.cache_bytes || 0)} file cache`;
  }
  worker.onmessage = async ({data}) => {
    if (data.type === "ready") {
      engineReady = true;
      updateBusy();
      $("engine-status").textContent = "Go WebAssembly ready · no network services";
      try {
        const response = await fetch("api/config");
        if (response.ok) {
          const config = await response.json();
          if (config.atlas && !manualAtlasChoice) await openAtlas("openURL", {...config.atlas, url:new URL(config.atlas.url, location.href).href});
        }
      } catch (error) { message(`Could not open the bundled atlas: ${error.message}`, true); }
      return;
    }
    if (data.type === "fatal") { failEngine(data.error); return; }
    const waiting = pending.get(data.id);
    if (!waiting) return;
    pending.delete(data.id); pendingCount--;
    updateBusy();
    $("engine-status").textContent = statsText(data.stats);
    $("engine-status").title = data.stats ? JSON.stringify(data.stats) : "";
    if (data.ok) waiting.resolve(data); else waiting.reject(new Error(data.error || "Lookup failed."));
  };
  worker.onerror = event => failEngine(event.message || "The WebAssembly worker stopped.");
  function failEngine(error) {
    engineReady = false;
    for (const waiting of pending.values()) waiting.reject(new Error(error));
    pending.clear(); pendingCount = 0; updateBusy();
    $("engine-status").textContent = "Engine unavailable";
    message(error, true); toast("The lookup engine could not start.");
  }

  async function openAtlas(command, options, file) {
    const oldAtlas = atlas;
    message(""); toast("Opening the local atlas…", true);
    try {
      const response = await rpc(command, options, file);
      atlas = response.result;
      $("atlas-name").textContent = atlas.name;
      $("atlas-status").classList.add("ready");
      const actualStates = atlas.states.filter(s => s !== "ZZ");
      $("atlas-detail").textContent = `${actualStates.length} states · ${bytes(atlas.size)} · ${Number(atlas.features).toLocaleString()} polygons`;
      $("atlas-detail").title = atlas.states.map(s => stateNames[s] || s).join(", ");
      clearResult();
      toast("Atlas ready. Click the map or enter coordinates.");
    } catch (error) {
      atlas = oldAtlas;
      message(`${error.message}${oldAtlas ? " Your previous atlas is still open." : ""}`, true);
      toast("The atlas could not be opened.");
    }
  }
  $("open-atlas").addEventListener("click", () => {manualAtlasChoice=true;$("atlas-file").click();});
  $("atlas-file").addEventListener("change", async event => {
    const file = event.target.files[0];
    if (file) await openAtlas("open", {}, file);
    event.target.value = "";
  });

  function switchMode(next, focus = false) {
    inputModes[kind].value = $("query").value;
    kind = next;
    for (const button of document.querySelectorAll("[role=tab]")) {
      const active = button.dataset.kind === kind;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
    }
    $("search-panel").setAttribute("aria-labelledby", `tab-${kind}`);
    $("query-label").textContent = inputModes[kind].label;
    $("query-help").textContent = inputModes[kind].help;
    $("query").placeholder = inputModes[kind].placeholder;
    $("query").value = inputModes[kind].value;
    $("id-kind-field").classList.toggle("hidden", kind !== "plss");
    message("");
    if (focus) $("query").focus();
  }
  for (const button of document.querySelectorAll("[role=tab]")) {
    button.addEventListener("click", () => switchMode(button.dataset.kind));
    button.addEventListener("keydown", event => {
      if (!["ArrowLeft","ArrowRight","Home","End"].includes(event.key)) return;
      event.preventDefault();
      const keys = Object.keys(inputModes), index = keys.indexOf(kind);
      const next = event.key === "Home" ? 0 : event.key === "End" ? 2 : (index + (event.key === "ArrowRight" ? 1 : 2)) % 3;
      switchMode(keys[next]); $("tab-"+keys[next]).focus();
    });
  }
  $("search-form").addEventListener("submit", event => { event.preventDefault(); search(); });
  $("query").addEventListener("keydown", event => {
    if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); search(); }
  });
  for (const button of document.querySelectorAll("[data-example]")) {
    button.addEventListener("click", () => {
      switchMode("latlon");
      $("query").value = examples[button.dataset.example].join(", ");
      search();
    });
  }
  async function search(point) {
    if (!engineReady || busy) return;
    message(""); clearResult();
    toast("Finding the published survey…", true);
    try {
      const command = point ? "lookup" : kind === "plss" ? "find" : "convert";
      const payload = point ? {latitude:point.lat,longitude:MapGeometry.normalizeLongitude(point.lng)} : kind === "plss" ? {kind:$("id-kind").value,id:$("query").value.trim()} : {kind,value:$("query").value.trim()};
      const response = await rpc(command, payload);
      renderResult(response.result, response.elapsed_ms, !!point);
      if (response.result.notes?.length) message(response.result.notes.join(" "));
      toast("");
    } catch (error) { message(error.message, true); toast("Check the input and try again."); }
  }
  function clearResult() {
    lastResult = null;
    document.querySelector(".sidebar").classList.remove("has-result");
    for (const layer of polygonLayers) map.removeLayer(layer);
    polygonLayers = [];
    if (pointMarker) { map.removeLayer(pointMarker); pointMarker = null; }
    $("results").classList.add("hidden"); $("fit-selection").classList.add("hidden");
    $("export-button").classList.add("hidden");
    $("matches").replaceChildren(); $("coordinate-results").replaceChildren();
    $("welcome").classList.remove("hidden");
    $("map-subtitle").textContent = "Click a point. Discover its description.";
  }
  const copySVG = '<svg viewBox="0 0 20 20" aria-hidden="true"><rect x="7" y="7" width="10" height="11" rx="1.5"/><path d="M13 7V3H3v11h4"/></svg>';
  function coordinateRow(label, value) {
    const row = element("div", "coordinate-row");
    const detail = element("div");
    detail.append(element("small", "", label), element("code", "", value));
    const copy = element("button", "copy-button");
    copy.innerHTML = copySVG;
    copy.setAttribute("aria-label", `Copy ${label.toLowerCase()}`);
    copy.title = `Copy ${label.toLowerCase()}`;
    copy.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(value); toast("Coordinate copied."); }
      catch { message("Copy is unavailable in this browser. Select the coordinate text to copy it."); }
    });
    row.append(detail, copy); return row;
  }
  function renderCoordinates(coordinates) {
    $("coordinate-results").replaceChildren();
    if (!coordinates) return;
    const rows = [["DECIMAL DEGREES",coordinates.dd],["DEGREES · MINUTES · SECONDS",coordinates.dms],["DEGREES · DECIMAL MINUTES",coordinates.ddm],["UTM · WGS 84",coordinates.utm_text || "Outside the UTM latitude range"],["ISO 6709",coordinates.iso6709]];
    for (const [label,value] of rows) $("coordinate-results").append(coordinateRow(label,value));
  }
  function renderResult(result, elapsed, mapClick) {
    lastResult = result; selectedIndex = 0;
    document.querySelector(".sidebar").classList.add("has-result");
    document.querySelector(".sidebar-scroll").scrollTop = 0;
    $("results").classList.remove("hidden"); $("welcome").classList.add("hidden");
    $("query-duration").textContent = duration(elapsed);
    $("query-duration").title = "Worker operation time, including local reads, conversion, and geometry";
    renderCoordinates(result.coordinates);
    $("match-count").textContent = result.matches.length;
    if (result.coordinates) {
      const point = result.coordinates;
      const lon = point.longitude + 360*Math.round((map.getCenter().lng-point.longitude)/360);
      if (!mapClick) map.setView([point.latitude,lon],Math.max(map.getZoom(),9),{animate:false});
      pointMarker = L.marker([point.latitude,lon],{icon:L.divIcon({className:"location-pin",html:"<span></span>",iconSize:[16,16],iconAnchor:[8,8]}),keyboard:true}).addTo(map);
      pointMarker.bindTooltip(result.representative ? "Interior representative point" : "Your coordinate",{direction:"top",offset:[0,-12],className:"point-tooltip"});
      $("map-coordinate").textContent = `${point.latitude.toFixed(6)}, ${point.longitude.toFixed(6)} · WGS 84`;
    }
    if (!result.matches.length) {
      const text = !atlas ? "Coordinates converted. Choose a local atlas to find the published PLSS description." : result.no_match ? "No published PLSS polygon contains this point, or the identifier was not found in this atlas. Coverage can have gaps; some parts of the U.S. do not use PLSS." : "No published polygon returned.";
      $("matches").append(element("div","empty-result",text));
      $("map-subtitle").textContent = atlas ? "No published polygon at this location." : "Open an atlas to explore PLSS coverage.";
    }
    result.matches.forEach((match,index) => {
      const card = element("button","match-card" + (index===0?" selected":""));
      const meta = element("div","match-meta");
      meta.append(element("span","",stateNames[match.state]||match.state),element("span","",match.layer));
      const actualID = Object.entries(match.identifiers || {}).find(([,value]) => value && value === match.id);
      const idText = match.id ? `${actualID ? actualID[0] + " ID · " : ""}${match.id}` : "Source record without an identifier";
      const fallbackTitle = {intersected:"Published subdivision",section:"Published section",township:"Published township"}[match.layer] || "Published survey polygon";
      const title = match.label && match.label !== match.id ? match.label : fallbackTitle;
      card.append(meta,element("strong","",title),element("code","",idText),element("div","match-action","View survey boundary ↗"));
      card.addEventListener("click", () => selectMatch(index,true));
      $("matches").append(card);
    });
    if (result.matches.length) {
      drawPolygons();
      $("fit-selection").classList.remove("hidden");
      $("map-subtitle").textContent = `${result.matches.length} published ${result.matches.length===1?"polygon":"polygons"} · source coordinates preserved`;
      if (!mapClick) fitSelection();
    }
    $("export-button").classList.remove("hidden");
  }
  function drawPolygons() {
    for (const layer of polygonLayers) map.removeLayer(layer);
    polygonLayers = [];
    if (!lastResult) return;
    lastResult.matches.forEach((match,index) => {
      const rings = MapGeometry.displayRings(match.geometry.rings, map.getCenter().lng);
      const layer = L.polygon(rings,{pane:"survey",color:index===selectedIndex?"#af622f":"#708e57",weight:index===selectedIndex?2.4:1.3,fillColor:index===selectedIndex?"#d69a60":"#9bb57d",fillOpacity:.22,fillRule:"evenodd",interactive:false,smoothFactor:0}).addTo(map);
      polygonLayers.push(layer);
    });
  }
  function selectMatch(index, fit) {
    if (!lastResult) return;
    selectedIndex = index;
    $("matches").querySelectorAll(".match-card").forEach((card,i)=>card.classList.toggle("selected",i===index));
    drawPolygons();
    if (lastResult.representative) {
      const coordinate = lastResult.matches[index].representative;
      renderCoordinates(coordinate);
      if (pointMarker) pointMarker.setLatLng([coordinate.latitude,coordinate.longitude+360*Math.round((map.getCenter().lng-coordinate.longitude)/360)]);
      $("map-coordinate").textContent=`${coordinate.latitude.toFixed(6)}, ${coordinate.longitude.toFixed(6)} · WGS 84`;
    }
    if (fit) fitSelection();
  }
  function fitSelection() {
    const layer = polygonLayers[selectedIndex];
    if (layer && layer.getBounds().isValid()) map.fitBounds(layer.getBounds(),{paddingTopLeft:[45,140],paddingBottomRight:[45,90],maxZoom:17,animate:false});
  }
  $("fit-selection").addEventListener("click",fitSelection);
  $("export-button").addEventListener("click",()=>{
    if (!lastResult) return;
    const selected=lastResult.matches[selectedIndex];
    const result=lastResult.representative && selected ? {...lastResult,coordinates:selected.representative} : lastResult;
    const exported = {application:"PLSconvertmyPLSS",atlas:atlas?{name:atlas.name,sources:atlas.sources}:null,selected_match_index:selected?selectedIndex:null,result};
    const url = URL.createObjectURL(new Blob([JSON.stringify(exported,null,2)],{type:"application/json"}));
    const link = element("a"); link.href=url;link.download="plss-result.json";document.body.append(link);link.click();link.remove();
    setTimeout(()=>URL.revokeObjectURL(url),1000);
  });
  map.on("click",event=>{
    if (!engineReady || busy) return;
    switchMode("latlon");
    $("query").value=`${event.latlng.lat.toFixed(8)}, ${MapGeometry.normalizeLongitude(event.latlng.lng).toFixed(8)}`;
    search(event.latlng);
  });
  map.on("mousemove",event=>{
    $("map-coordinate").textContent=`${event.latlng.lat.toFixed(5)}, ${MapGeometry.normalizeLongitude(event.latlng.lng).toFixed(5)} · WGS 84`;
  });
  const regions = {us:[[39.7,-99.6],4],ak:[[63,-153],4],hi:[[20.8,-157],6]};
  for (const button of document.querySelectorAll("[data-region]")) button.addEventListener("click",()=>{
    const [center,zoom]=regions[button.dataset.region]; map.setView(center,zoom,{animate:false});
    document.querySelectorAll("[data-region]").forEach(b=>b.classList.toggle("active",b===button));
  });
  function drawGeography() {
    if (!geographyData) return;
    geography.clearLayers();
    const center=map.getCenter().lng;
    L.geoJSON(MapGeometry.shiftGeoJSON(geographyData.land,center),{style:{color:"#b7c5ac",weight:.65,fillColor:"#f2f2e7",fillOpacity:1},interactive:false,smoothFactor:.5}).addTo(geography);
    L.geoJSON(MapGeometry.shiftGeoJSON(geographyData.states,center),{style:{color:"#99ac87",weight:.8,fillColor:"#f7f6ec",fillOpacity:.35},interactive:false,smoothFactor:.25}).addTo(geography);
    L.geoJSON(MapGeometry.shiftGeoJSON(geographyData.lakes,center),{style:{color:"#bdcfc4",weight:.35,fillColor:"#e2ebe8",fillOpacity:1},interactive:false,smoothFactor:.5}).addTo(geography);
  }
  function drawLabels() {
    labels.clearLayers();
    const zoom=map.getZoom(), center=map.getCenter().lng;
    if(geographyData && zoom>=3.5 && zoom<=7){
      const smallStates=new Set(["RI","CT","DE","MD","NJ","MA","NH","VT","DC"]);
      for(const feature of geographyData.states.features){
        if(zoom<5.5 && smallStates.has(feature.properties.code))continue;
        const [lon,lat]=feature.properties.label;
        const point=[lat,lon+360*Math.round((center-lon)/360)];
        if(!map.getBounds().contains(point))continue;
        const text=element("span","",feature.properties.code);
        L.marker(point,{pane:"cityLabels",interactive:false,keyboard:false,icon:L.divIcon({className:"state-label",html:text.outerHTML,iconSize:[40,18],iconAnchor:[20,9]})}).addTo(labels);
      }
    }
    for(const feature of cityData) {
      if(zoom<Math.max(3.5,feature.properties.minZoom)) continue;
      if(zoom<4.5 && feature.properties.rank>2)continue;
      const [lon,lat]=feature.geometry.coordinates;
      const point=[lat,lon+360*Math.round((center-lon)/360)];
      if(!map.getBounds().pad(.1).contains(point)) continue;
      const html=element("span");html.append(element("i","city-dot"),document.createTextNode(feature.properties.name));
      L.marker(point,{pane:"cityLabels",interactive:false,keyboard:false,icon:L.divIcon({className:"city-label",html:html.outerHTML,iconSize:[130,18],iconAnchor:[3,9]})}).addTo(labels);
    }
  }
  map.on("moveend",()=>{
    drawGeography();drawPolygons();
    if(pointMarker){const p=pointMarker.getLatLng();const lon=MapGeometry.normalizeLongitude(p.lng);pointMarker.setLatLng([p.lat,lon+360*Math.round((map.getCenter().lng-lon)/360)]);}
    const center=map.getCenter(), lon=MapGeometry.normalizeLongitude(center.lng);
    const region=center.lat>50 && (lon < -130 || lon > 170) ? "ak" : center.lat>17 && center.lat<25 && lon>-165 && lon<-150 ? "hi" : center.lat>23 && center.lat<51 && lon>-130 && lon<-60 ? "us" : "";
    document.querySelectorAll("[data-region]").forEach(button=>button.classList.toggle("active",button.dataset.region===region));
    drawLabels();
  });
  Promise.all(["land","states","lakes","cities"].map(async name=>{
    const response=await fetch(`data/${name}.geojson`);
    if(!response.ok)throw new Error(`The bundled ${name} map could not be loaded.`);
    return [name,await response.json()];
  })).then(entries=>{
    geographyData=Object.fromEntries(entries);cityData=geographyData.cities.features;
    drawGeography();drawLabels();
  }).catch(error=>message(error.message,true));
  $("about-button").addEventListener("click",()=>$("about-dialog").showModal());
  $("close-about").addEventListener("click",()=>$("about-dialog").close());
  $("about-dialog").addEventListener("click",event=>{if(event.target===$("about-dialog"))$("about-dialog").close();});
  updateBusy();
})();
