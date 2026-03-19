package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	StopsLignesURL = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/arrets-lignes/exports/json?limit=-1"
	TracesURL      = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/traces-des-lignes-de-transport-en-commun-idfm/exports/json?limit=-1"
	RoutesFile     = "optimized_routes.json"
	RoutesBackup   = "optimized_routes.backup.json"
)

type Variant struct {
	ID    string   `json:"id"`
	Stops []string `json:"stops"`
}

type OptimizedLine struct {
	ID                    int        `json:"id"`
	RouteID               string     `json:"route_id"`
	ShortName             string     `json:"short_name"`
	Variants              []Variant  `json:"variants"`
	OptimalInfrastructure [][]string `json:"optimal_infrastructure"`
}

type stopPoint struct {
	ID  string
	Lat float64
	Lon float64
}

type geoPoint struct {
	Lat float64
	Lon float64
}

type projectedStop struct {
	ID       string
	Distance float64
	Position float64
}

func FetchRoutes() {
	prepareBackupOrder(RoutesFile, RoutesBackup)

	fmt.Println("⏳ Récupération des arrêts-lignes depuis IDFM...")
	respStops, err := http.Get(StopsLignesURL)
	if err != nil {
		checkErrOrder(err, RoutesFile, RoutesBackup)
		return
	}
	defer respStops.Body.Close()

	var rawStops []map[string]interface{}
	json.NewDecoder(respStops.Body).Decode(&rawStops)

	routeStops := make(map[string][]stopPoint)
	stopNameMap := make(map[string]string) // stop_id → stop_name
	for _, s := range rawStops {
		stopID := fmt.Sprint(s["stop_id"])
		routeID := normalizeRouteID(fmt.Sprint(s["id"]))
		stopName := fmt.Sprint(s["stop_name"])
		var lat, lon float64
		if geo, ok := s["pointgeo"].(map[string]interface{}); ok {
			lat, _ = geo["lat"].(float64)
			lon, _ = geo["lon"].(float64)
		} else {
			lat, _ = strconv.ParseFloat(fmt.Sprint(s["stop_lat"]), 64)
			lon, _ = strconv.ParseFloat(fmt.Sprint(s["stop_lon"]), 64)
		}

		if stopID != "" && stopID != "<nil>" && routeID != "" && lat != 0 {
			routeStops[routeID] = append(routeStops[routeID], stopPoint{
				ID:  stopID,
				Lat: lat,
				Lon: lon,
			})
		}
		if stopID != "" && stopID != "<nil>" && stopName != "" && stopName != "<nil>" {
			stopNameMap[stopID] = stopName
		}
	}

	fmt.Println("⏳ Récupération des tracés depuis IDFM...")
	respTraces, err := http.Get(TracesURL)
	if err != nil {
		checkErrOrder(err, RoutesFile, RoutesBackup)
		return
	}
	defer respTraces.Body.Close()

	var rawTraces []map[string]interface{}
	json.NewDecoder(respTraces.Body).Decode(&rawTraces)

	rawVariantsMap := make(map[string][][]string)
	lineNames := make(map[string]string)

	// Build quick lookups from lines.json (mode + short name).
	linesFileData, _ := os.ReadFile(LinesFile)
	var linesInfo []map[string]interface{}
	_ = json.Unmarshal(linesFileData, &linesInfo)
	routeMode := make(map[string]TransportMode)
	metroRouteIDs := make(map[string]bool)
	for _, l := range linesInfo {
		id := fmt.Sprint(l["id"])
		mode := TransportMode(fmt.Sprint(l["mode"]))
		routeMode[id] = mode
		if mode == TransportModeMetro {
			metroRouteIDs[id] = true
		}
	}

	for _, item := range rawTraces {
		rawID := fmt.Sprint(item["id_ilico"])
		if rawID == "" || rawID == "<nil>" {
			continue
		}

		routeID := "IDFM:" + rawID
		lineNames[routeID] = fmt.Sprint(item["route_short_name"])

		shape, _ := item["shape"].(map[string]interface{})
		geometry, _ := shape["geometry"].(map[string]interface{})
		polylines := polylinesFromGeometry(geometry)
		if len(polylines) == 0 {
			continue
		}

		for _, polyline := range polylines {
			if len(polyline) < 2 {
				continue
			}
			currentVariant := buildVariantFromPolyline(routeStops[routeID], polyline, routeMode[routeID])
			if len(currentVariant) >= 2 {
				rawVariantsMap[routeID] = append(rawVariantsMap[routeID], currentVariant)
			}
		}
	}

	// --- NORMALISATION DES ARRÊTS DE MÉTRO PAR NOM ---
	// Les arrêts de métro existent en double (un par sens) avec des IDs différents
	// mais le même nom. On normalise pour que les deux sens partagent les mêmes IDs.
	for routeID, variants := range rawVariantsMap {
		if !metroRouteIDs[routeID] {
			continue
		}
		// Prioriser le variant le plus long pour choisir l'ID canonique
		sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })

		// nom → premier ID rencontré = ID canonique
		nameToCanonical := make(map[string]string)
		for _, v := range variants {
			for _, stopID := range v {
				if name := stopNameMap[stopID]; name != "" {
					if _, exists := nameToCanonical[name]; !exists {
						nameToCanonical[name] = stopID
					}
				}
			}
		}

		// Remplacer dans chaque variant les IDs "doublon" par l'ID canonique
		normalized := make([][]string, 0, len(variants))
		for _, v := range variants {
			seenIDs := make(map[string]bool)
			var normalizedV []string
			for _, stopID := range v {
				canonicalID := stopID
				if name := stopNameMap[stopID]; name != "" {
					if cid, ok := nameToCanonical[name]; ok {
						canonicalID = cid
					}
				}
				if !seenIDs[canonicalID] {
					normalizedV = append(normalizedV, canonicalID)
					seenIDs[canonicalID] = true
				}
			}
			if len(normalizedV) >= 2 {
				normalized = append(normalized, normalizedV)
			}
		}
		rawVariantsMap[routeID] = normalized
	}

	var routeIDs []string
	for rID := range rawVariantsMap {
		routeIDs = append(routeIDs, rID)
	}
	sort.Strings(routeIDs)

	var finalData []OptimizedLine
	idCounter := 1

	for _, routeID := range routeIDs {
		variants := rawVariantsMap[routeID]

		// --- LOGIQUE INFRASTRUCTURE OPTIMALE (CORRIGÉE) ---

		// 1. Collecter tous les segments uniques possibles
		allPossibleSegments := make(map[string][]string)
		for _, v := range variants {
			for i := 0; i < len(v)-1; i++ {
				pair := []string{v[i], v[i+1]}
				sort.Strings(pair)
				key := pair[0] + "--" + pair[1]
				allPossibleSegments[key] = []string{pair[0], pair[1]}
			}
		}

		// 2. Filtre anti-saut : supprimer les segments qui "sautent" une gare existante dans un autre variant
		infrastructureMap := make(map[string][]string)

		for _, pair := range allPossibleSegments {
			idA, idB := pair[0], pair[1]
			isJump := false

			for _, v := range variants {
				idxA, idxB := -1, -1
				for i, stopID := range v {
					if stopID == idA {
						idxA = i
					}
					if stopID == idB {
						idxB = i
					}
				}

				if idxA != -1 && idxB != -1 {
					dist := idxA - idxB
					if dist < 0 {
						dist = -dist
					}
					if dist > 1 {
						isJump = true
						break
					}
				}
			}

			if !isJump {
				// 🔒 DÉDUPLICATION FINALE
				key := idA + "--" + idB
				infrastructureMap[key] = []string{idA, idB}
			}
		}

		// 3. Filtre anti-outlier géographique : supprimer les liens beaucoup trop longs
		// (symptôme typique d'un variant erroné).
		coordsByStopID := make(map[string]stopPoint)
		for _, s := range routeStops[routeID] {
			if s.ID == "" {
				continue
			}
			if _, ok := coordsByStopID[s.ID]; !ok {
				coordsByStopID[s.ID] = s
			}
		}

		adjDistances := make([]float64, 0, 256)
		for _, v := range variants {
			for i := 0; i < len(v)-1; i++ {
				a, okA := coordsByStopID[v[i]]
				b, okB := coordsByStopID[v[i+1]]
				if !okA || !okB {
					continue
				}
				adjDistances = append(adjDistances, approxDistanceMeters(a.Lat, a.Lon, b.Lat, b.Lon))
			}
		}
		sort.Float64s(adjDistances)
		maxReasonable := math.MaxFloat64
		if len(adjDistances) >= 10 {
			p90 := adjDistances[int(0.9*float64(len(adjDistances)-1))]
			maxReasonable = math.Max(p90*2.0, p90+3000.0)
		}

		var infrastructure [][]string
		for _, seg := range infrastructureMap {
			if maxReasonable != math.MaxFloat64 {
				a, okA := coordsByStopID[seg[0]]
				b, okB := coordsByStopID[seg[1]]
				if okA && okB {
					if approxDistanceMeters(a.Lat, a.Lon, b.Lat, b.Lon) > maxReasonable {
						continue
					}
				}
			}
			infrastructure = append(infrastructure, seg)
		}

		// --- LOGIQUE VARIANTS (EXISTANTE) ---
		sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
		var filtered [][]string
		for _, v := range variants {
			isSub := false
			for _, master := range filtered {
				if isSubSequence(v, master) {
					isSub = true
					break
				}
			}
			if !isSub && !isDuplicate(filtered, v) {
				filtered = append(filtered, v)
			}
		}

		var variantObjects []Variant
		for i, v := range filtered {
			variantObjects = append(variantObjects, Variant{
				ID:    fmt.Sprintf("%s_%d", routeID, i),
				Stops: v,
			})
		}

		finalData = append(finalData, OptimizedLine{
			ID:                    idCounter,
			RouteID:               routeID,
			ShortName:             lineNames[routeID],
			Variants:              variantObjects,
			OptimalInfrastructure: infrastructure,
		})
		idCounter++
	}

	data, _ := json.MarshalIndent(finalData, "", "  ")
	os.WriteFile(RoutesFile, data, 0644)
	fmt.Printf("✅ %d lignes traitées. Infrastructure adjacente générée.\n", len(finalData))
}

// ... (Gardez les fonctions utilitaires preparesBackupOrder, checkErrOrder, etc. à l'identique)

func prepareBackupOrder(file string, backup string) {
	if _, err := os.Stat(file); err == nil {
		input, _ := os.ReadFile(file)
		os.WriteFile(backup, input, 0644)
	}
}

func checkErrOrder(err error, file string, backup string) {
	fmt.Printf("❌ Erreur : %v\n", err)
	if _, statErr := os.Stat(backup); statErr == nil {
		input, _ := os.ReadFile(backup)
		os.WriteFile(file, input, 0644)
	}
}

func isSubSequence(sub []string, main []string) bool {
	if len(sub) >= len(main) {
		return false
	}
	sStr := strings.Join(sub, "|")
	mStr := strings.Join(main, "|")
	return strings.Contains(mStr, sStr)
}

func isDuplicate(existing [][]string, newVar []string) bool {
	newHash := hashSequence(newVar)
	for _, e := range existing {
		if hashSequence(e) == newHash {
			return true
		}
	}
	return false
}

func hashSequence(s []string) string {
	h := md5.New()
	io.WriteString(h, strings.Join(s, ","))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func normalizeRouteID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "<nil>" {
		return ""
	}
	if strings.HasPrefix(raw, "IDFM:") {
		return raw
	}
	return "IDFM:" + raw
}

func buildVariantFromSegment(stops []stopPoint, rawPoints []interface{}) []string {
	return buildVariantFromPolyline(stops, toPolyline(rawPoints), TransportMode(""))
}

func buildVariantFromPolyline(stops []stopPoint, polyline []geoPoint, mode TransportMode) []string {
	if len(polyline) < 2 || len(stops) == 0 {
		return nil
	}

	projected := make([]projectedStop, 0, len(stops))
	for _, s := range stops {
		distance, position := nearestDistanceAndPosition(polyline, s)
		projected = append(projected, projectedStop{
			ID:       s.ID,
			Distance: distance,
			Position: position,
		})
	}

	sort.Slice(projected, func(i, j int) bool {
		if projected[i].Position == projected[j].Position {
			return projected[i].Distance < projected[j].Distance
		}
		return projected[i].Position < projected[j].Position
	})

	thresholds := thresholdsForMode(mode)
	uniqueStops := uniqueStopCount(stops)
	minStops := 2
	if uniqueStops > 50 {
		minStops = 15
	} else if uniqueStops > 20 {
		minStops = 8
	}

	best := make([]string, 0)
	for _, threshold := range thresholds {
		candidate := collectProjectedStops(projected, threshold)
		if len(candidate) >= minStops {
			best = candidate
			break // thresholds are increasing; first valid is best (most strict)
		}
	}

	if len(best) >= 2 {
		return best
	}

	// IMPORTANT: no fallback that includes far-away stops.
	return nil
}

func thresholdsForMode(mode TransportMode) []float64 {
	// Caps are intentionally conservative: large thresholds easily pull in stops from
	// nearby branches and create non-existent direct links.
	switch mode {
	case TransportModeMetro:
		return []float64{80, 120, 160, 200, 250, 300, 400}
	case TransportModeRer, TransportModeTransilien, TransportModeTer, TransportModeTramway:
		return []float64{120, 200, 300, 400, 600, 800}
	case TransportModeCableway, TransportModeNavette:
		return []float64{80, 120, 200, 300, 400, 600}
	case TransportModeBus:
		return []float64{40, 60, 80, 100, 150, 200, 300}
	default:
		return []float64{120, 200, 300, 400, 600, 800}
	}
}

func uniqueStopCount(stops []stopPoint) int {
	seen := make(map[string]bool, len(stops))
	for _, s := range stops {
		if s.ID == "" {
			continue
		}
		seen[s.ID] = true
	}
	return len(seen)
}

func polylinesFromGeometry(geometry map[string]interface{}) [][]geoPoint {
	if geometry == nil {
		return nil
	}
	geomType := fmt.Sprint(geometry["type"])
	coords := geometry["coordinates"]

	switch geomType {
	case "LineString":
		points, ok := coords.([]interface{})
		if !ok {
			return nil
		}
		p := toPolyline(points)
		if len(p) < 2 {
			return nil
		}
		return [][]geoPoint{p}
	case "MultiLineString":
		segments, ok := coords.([]interface{})
		if !ok {
			return nil
		}
		polylines := make([][]geoPoint, 0, len(segments))
		for _, segRaw := range segments {
			segPoints, ok := segRaw.([]interface{})
			if !ok {
				continue
			}
			p := toPolyline(segPoints)
			if len(p) >= 2 {
				polylines = append(polylines, p)
			}
		}
		return polylines
	default:
		// Best-effort: inspect nesting to decide LineString vs MultiLineString.
		segments, ok := coords.([]interface{})
		if !ok || len(segments) == 0 {
			return nil
		}
		first, ok := segments[0].([]interface{})
		if !ok || len(first) == 0 {
			return nil
		}
		// If first[0] is a number => coords is a point => segments is actually a LineString.
		if _, okNum := first[0].(float64); okNum {
			p := toPolyline(segments)
			if len(p) < 2 {
				return nil
			}
			return [][]geoPoint{p}
		}
		// Otherwise treat as MultiLineString.
		polylines := make([][]geoPoint, 0, len(segments))
		for _, segRaw := range segments {
			segPoints, ok := segRaw.([]interface{})
			if !ok {
				continue
			}
			p := toPolyline(segPoints)
			if len(p) >= 2 {
				polylines = append(polylines, p)
			}
		}
		return polylines
	}
}

func approxDistanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	// Equirectangular approximation (good enough at IDF scale).
	refLat := (lat1 + lat2) / 2.0
	x1, y1 := projectToMeters(lat1, lon1, refLat)
	x2, y2 := projectToMeters(lat2, lon2, refLat)
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}

func collectProjectedStops(projected []projectedStop, maxDistance float64) []string {
	seen := make(map[string]bool)
	ordered := make([]string, 0, len(projected))

	for _, p := range projected {
		if p.Distance > maxDistance {
			continue
		}
		if seen[p.ID] {
			continue
		}
		ordered = append(ordered, p.ID)
		seen[p.ID] = true
	}

	return ordered
}

func toPolyline(rawPoints []interface{}) []geoPoint {
	polyline := make([]geoPoint, 0, len(rawPoints))
	for _, raw := range rawPoints {
		coord, ok := raw.([]interface{})
		if !ok || len(coord) < 2 {
			continue
		}

		lon, okLon := coord[0].(float64)
		lat, okLat := coord[1].(float64)
		if !okLon || !okLat {
			continue
		}

		polyline = append(polyline, geoPoint{Lat: lat, Lon: lon})
	}
	return polyline
}

func nearestDistanceAndPosition(polyline []geoPoint, stop stopPoint) (float64, float64) {
	if len(polyline) < 2 {
		return math.MaxFloat64, 0
	}

	minDistance := math.MaxFloat64
	bestPosition := 0.0
	cumulative := 0.0

	for i := 0; i < len(polyline)-1; i++ {
		a := polyline[i]
		b := polyline[i+1]

		distance, segmentPosition, segmentLength := pointToSegmentDistance(stop, a, b)
		if distance < minDistance {
			minDistance = distance
			bestPosition = cumulative + segmentPosition
		}

		cumulative += segmentLength
	}

	return minDistance, bestPosition
}

func pointToSegmentDistance(p stopPoint, a geoPoint, b geoPoint) (float64, float64, float64) {
	ax, ay := projectToMeters(a.Lat, a.Lon, a.Lat)
	bx, by := projectToMeters(b.Lat, b.Lon, a.Lat)
	px, py := projectToMeters(p.Lat, p.Lon, a.Lat)

	abx := bx - ax
	aby := by - ay
	abLen2 := abx*abx + aby*aby
	if abLen2 == 0 {
		dx := px - ax
		dy := py - ay
		d := math.Sqrt(dx*dx + dy*dy)
		return d, 0, 0
	}

	t := ((px-ax)*abx + (py-ay)*aby) / abLen2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}

	projX := ax + t*abx
	projY := ay + t*aby
	dx := px - projX
	dy := py - projY
	distance := math.Sqrt(dx*dx + dy*dy)
	segmentLength := math.Sqrt(abLen2)
	position := t * segmentLength

	return distance, position, segmentLength
}

func projectToMeters(lat float64, lon float64, refLat float64) (float64, float64) {
	latRad := refLat * math.Pi / 180.0
	x := lon * 111320.0 * math.Cos(latRad)
	y := lat * 110540.0
	return x, y
}
