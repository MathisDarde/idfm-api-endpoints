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
			currentVariant = applyRouteSpecificVariantFixes(routeID, currentVariant)
			if len(currentVariant) >= 2 {
				rawVariantsMap[routeID] = append(rawVariantsMap[routeID], currentVariant)
			}
		}
	}

	// Route-specific normalization pass after all variants were collected.
	for routeID, variants := range rawVariantsMap {
		out := make([][]string, 0, len(variants))
		for _, v := range variants {
			out = append(out, applyRouteSpecificVariantFixes(routeID, v))
		}
		rawVariantsMap[routeID] = out
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

	stopsByID := make(map[string]stopPoint, len(stops))
	for _, s := range stops {
		if s.ID == "" {
			continue
		}
		if _, ok := stopsByID[s.ID]; !ok {
			stopsByID[s.ID] = s
		}
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
	minStops := minStopsForMode(mode, uniqueStops)

	best, bestThreshold, ok := pickBestCandidate(projected, thresholds, mode, minStops)
	if !ok {
		return nil
	}

	refined := refineVariant(projected, stopsByID, best, bestThreshold, mode)
	if len(refined) >= 2 {
		return refined
	}
	return best
}

type candidateInfo struct {
	IDs       []string
	Distances []float64
	Threshold float64
	Count     int
	MaxDist   float64
	P90Dist   float64
	FarCount  int
}

func minStopsForMode(mode TransportMode, uniqueStops int) int {
	// IMPORTANT:
	// - The polyline we process can represent only a branch/portion of the full route.
	// - Using a large minStops based on the whole line forces thresholds up and
	//   pulls stops from nearby branches (exactly the bug we want to avoid).
	base := 2
	if uniqueStops >= 20 {
		base = 3
	}
	if uniqueStops >= 40 {
		base = 4
	}
	switch mode {
	case TransportModeMetro:
		base++
	case TransportModeRer, TransportModeTransilien, TransportModeTer, TransportModeTramway:
		base++
	}
	if base > 6 {
		base = 6
	}
	return base
}

func pickBestCandidate(projected []projectedStop, thresholds []float64, mode TransportMode, minStops int) ([]string, float64, bool) {
	if len(thresholds) == 0 {
		return nil, 0, false
	}

	infos := make([]candidateInfo, 0, len(thresholds))
	for _, t := range thresholds {
		ids, dists := collectProjectedStopsWithDistances(projected, t)
		if len(ids) < minStops {
			continue
		}
		info := candidateInfo{
			IDs:       ids,
			Distances: dists,
			Threshold: t,
			Count:     len(ids),
			MaxDist:   maxFloatSlice(dists),
			P90Dist:   percentile(dists, 0.9),
			FarCount:  farCountForMode(dists, mode, t),
		}
		infos = append(infos, info)
	}

	if len(infos) == 0 {
		return nil, 0, false
	}

	// Prefer candidates that:
	// 1) include as few "far" stops as possible (branch contamination)
	// 2) include many stops
	// 3) have small distance dispersion
	// 4) use smaller thresholds
	sort.Slice(infos, func(i, j int) bool {
		a := infos[i]
		b := infos[j]
		if a.FarCount != b.FarCount {
			return a.FarCount < b.FarCount
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.P90Dist != b.P90Dist {
			return a.P90Dist < b.P90Dist
		}
		if a.MaxDist != b.MaxDist {
			return a.MaxDist < b.MaxDist
		}
		return a.Threshold < b.Threshold
	})

	best := infos[0]
	return best.IDs, best.Threshold, true
}

func farCountForMode(dists []float64, mode TransportMode, threshold float64) int {
	if len(dists) == 0 {
		return 0
	}
	cap := farDistanceCapForMode(mode)
	// Also treat near-threshold stops as suspicious when threshold is large.
	nearThr := threshold * 0.9
	if nearThr < cap {
		cap = nearThr
	}
	count := 0
	for _, d := range dists {
		if d > cap {
			count++
		}
	}
	return count
}

func farDistanceCapForMode(mode TransportMode) float64 {
	switch mode {
	case TransportModeMetro:
		return 220
	case TransportModeRer, TransportModeTransilien, TransportModeTer, TransportModeTramway:
		return 420
	case TransportModeBus:
		return 160
	default:
		return 420
	}
}

func collectProjectedStopsWithDistances(projected []projectedStop, maxDistance float64) ([]string, []float64) {
	seen := make(map[string]bool)
	ordered := make([]string, 0, len(projected))
	dists := make([]float64, 0, len(projected))

	for _, p := range projected {
		if p.Distance > maxDistance {
			continue
		}
		if seen[p.ID] {
			continue
		}
		ordered = append(ordered, p.ID)
		dists = append(dists, p.Distance)
		seen[p.ID] = true
	}

	return ordered, dists
}

func refineVariant(projected []projectedStop, stopsByID map[string]stopPoint, ids []string, threshold float64, mode TransportMode) []string {
	if len(ids) < 2 {
		return ids
	}

	// Unique projected list (best distance per stop) for candidate insertion.
	bestProj := make(map[string]projectedStop, len(projected))
	for _, p := range projected {
		if existing, ok := bestProj[p.ID]; !ok || p.Distance < existing.Distance {
			bestProj[p.ID] = p
		}
	}
	uniqProj := make([]projectedStop, 0, len(bestProj))
	for _, p := range bestProj {
		uniqProj = append(uniqProj, p)
	}
	sort.Slice(uniqProj, func(i, j int) bool { return uniqProj[i].Position < uniqProj[j].Position })

	// 1) Fill large gaps with plausible missing stops.
	filled := fillLargeGaps(ids, uniqProj, stopsByID, threshold, mode)

	// 2) Remove obvious detours (typical symptom of branch contamination).
	pruned := pruneDetours(filled, stopsByID, bestProj, mode)

	return pruned
}

func fillLargeGaps(ids []string, uniqProj []projectedStop, stopsByID map[string]stopPoint, threshold float64, mode TransportMode) []string {
	if len(ids) < 2 {
		return ids
	}
	positions := make(map[string]float64, len(uniqProj))
	distToPolyline := make(map[string]float64, len(uniqProj))
	for _, p := range uniqProj {
		positions[p.ID] = p.Position
		distToPolyline[p.ID] = p.Distance
	}

	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}

	maxInsertDistance := gapInsertMaxDistanceForMode(mode, threshold)

	// Limit iterations to avoid pathological loops.
	for insertions := 0; insertions < 12; insertions++ {
		gapDistances := make([]float64, 0, len(ids)-1)
		for i := 0; i < len(ids)-1; i++ {
			a, okA := stopsByID[ids[i]]
			b, okB := stopsByID[ids[i+1]]
			if !okA || !okB {
				continue
			}
			gapDistances = append(gapDistances, approxDistanceMeters(a.Lat, a.Lon, b.Lat, b.Lon))
		}
		p90 := percentile(gapDistances, 0.9)
		gapThreshold := gapDistanceThresholdForMode(mode)
		if p90 > 0 {
			gapThreshold = math.Max(gapThreshold, math.Min(p90*1.7, p90+1500.0))
		}

		inserted := false
		for i := 0; i < len(ids)-1; i++ {
			ida := ids[i]
			idb := ids[i+1]
			a, okA := stopsByID[ida]
			b, okB := stopsByID[idb]
			if !okA || !okB {
				continue
			}
			dAB := approxDistanceMeters(a.Lat, a.Lon, b.Lat, b.Lon)
			if dAB <= gapThreshold {
				continue
			}

			posA, okPosA := positions[ida]
			posB, okPosB := positions[idb]
			if !okPosA || !okPosB {
				continue
			}
			minPos := posA
			maxPos := posB
			if minPos > maxPos {
				minPos, maxPos = maxPos, minPos
			}

			bestID := ""
			bestScore := math.MaxFloat64
			for _, p := range uniqProj {
				if seen[p.ID] {
					continue
				}
				if p.Distance > maxInsertDistance {
					continue
				}
				c, okC := stopsByID[p.ID]
				if !okC {
					continue
				}
				dAC := approxDistanceMeters(a.Lat, a.Lon, c.Lat, c.Lon)
				dCB := approxDistanceMeters(c.Lat, c.Lon, b.Lat, b.Lon)
				maxLeg := math.Max(dAC, dCB)
				// Accept only if it meaningfully reduces the large gap.
				if maxLeg >= dAB*0.85 {
					continue
				}
				within := p.Position > minPos && p.Position < maxPos
				// If the projected position isn't between the endpoints (polyline fold/branch),
				// only accept very strong splits.
				if !within && maxLeg >= dAB*0.60 {
					continue
				}
				// Prefer smaller max leg + smaller distance to polyline.
				score := maxLeg + distToPolyline[p.ID]
				if !within {
					score += 2000
				}
				if score < bestScore {
					bestScore = score
					bestID = p.ID
				}
			}

			if bestID != "" {
				// Insert and restart scan.
				ids = append(ids[:i+1], append([]string{bestID}, ids[i+1:]...)...)
				seen[bestID] = true
				inserted = true
				break
			}
		}

		if !inserted {
			break
		}
	}

	return ids
}

func pruneDetours(ids []string, stopsByID map[string]stopPoint, projByID map[string]projectedStop, mode TransportMode) []string {
	if len(ids) < 3 {
		return ids
	}
	detourThreshold := detourThresholdForMode(mode)

	changed := true
	for changed {
		changed = false
		out := make([]string, 0, len(ids))
		out = append(out, ids[0])
		for i := 1; i < len(ids)-1; i++ {
			a, okA := stopsByID[out[len(out)-1]]
			b, okB := stopsByID[ids[i]]
			c, okC := stopsByID[ids[i+1]]
			if !okA || !okB || !okC {
				out = append(out, ids[i])
				continue
			}
			dAB := approxDistanceMeters(a.Lat, a.Lon, b.Lat, b.Lon)
			dBC := approxDistanceMeters(b.Lat, b.Lon, c.Lat, c.Lon)
			dAC := approxDistanceMeters(a.Lat, a.Lon, c.Lat, c.Lon)
			detour := (dAB + dBC) - dAC

			if detour > detourThreshold {
				pb := projByID[ids[i]].Distance
				pa := projByID[out[len(out)-1]].Distance
				pc := projByID[ids[i+1]].Distance
				// Remove the middle stop if it's likely off the polyline compared to its neighbors.
				if pb > pa && pb > pc {
					changed = true
					continue
				}
			}
			out = append(out, ids[i])
		}
		out = append(out, ids[len(ids)-1])
		ids = out
	}

	return ids
}

func detourThresholdForMode(mode TransportMode) float64 {
	switch mode {
	case TransportModeMetro:
		return 700
	case TransportModeRer, TransportModeTransilien, TransportModeTer, TransportModeTramway:
		return 1500
	case TransportModeBus:
		return 500
	default:
		return 1500
	}
}

func gapDistanceThresholdForMode(mode TransportMode) float64 {
	// If two consecutive stops are farther apart than this, we try to insert missing
	// intermediate stops that project between them on the polyline.
	switch mode {
	case TransportModeMetro:
		return 1800
	case TransportModeTramway:
		return 2500
	case TransportModeBus:
		return 2000
	case TransportModeRer, TransportModeTransilien, TransportModeTer:
		return 3000
	default:
		return 3000
	}
}

func gapInsertMaxDistanceForMode(mode TransportMode, threshold float64) float64 {
	// Polylines are sometimes offset: allow a broader distance budget for gap filling
	// than for the initial variant selection.
	base := 600.0
	switch mode {
	case TransportModeMetro:
		base = 700
	case TransportModeTramway:
		base = 900
	case TransportModeBus:
		base = 700
	case TransportModeRer, TransportModeTransilien, TransportModeTer:
		base = 1800
	default:
		base = 1800
	}
	// Keep some link with the chosen threshold, but never below the mode base.
	return math.Max(base, threshold*3.0)
}

func maxFloatSlice(xs []float64) float64 {
	maxV := 0.0
	for _, v := range xs {
		if v > maxV {
			maxV = v
		}
	}
	return maxV
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cpy := make([]float64, len(xs))
	copy(cpy, xs)
	sort.Float64s(cpy)
	if p <= 0 {
		return cpy[0]
	}
	if p >= 1 {
		return cpy[len(cpy)-1]
	}
	idx := int(p * float64(len(cpy)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cpy) {
		idx = len(cpy) - 1
	}
	return cpy[idx]
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

func applyRouteSpecificVariantFixes(routeID string, variant []string) []string {
	// Keep this narrowly scoped to known problematic cases.
	if routeID != "IDFM:C01742" || len(variant) < 2 {
		return variant
	}

	// RER A west branches: Poissy must connect via Achères Ville.
	poissy := "IDFM:monomodalStopPlace:47874"
	acheresVille := "IDFM:monomodalStopPlace:46647"

	idxPoissy := indexOfString(variant, poissy)
	if idxPoissy == -1 {
		return variant
	}

	variant = ensureAdjacent(variant, poissy, acheresVille)
	return dedupeStringsPreserveOrder(variant)
}

func ensureAdjacent(ids []string, pivot, required string) []string {
	i := indexOfString(ids, pivot)
	if i == -1 {
		return ids
	}
	// Already adjacent.
	if (i > 0 && ids[i-1] == required) || (i < len(ids)-1 && ids[i+1] == required) {
		return ids
	}

	// Prefer placing the required stop on the side that exists.
	insertAt := i
	if i == 0 {
		insertAt = 1
	}

	// If required exists elsewhere, move it.
	j := indexOfString(ids, required)
	if j != -1 {
		ids = append(ids[:j], ids[j+1:]...)
		if j < insertAt {
			insertAt--
		}
	}

	// Insert required.
	ids = append(ids[:insertAt], append([]string{required}, ids[insertAt:]...)...)
	return ids
}

func dedupeStringsPreserveOrder(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func indexOfString(xs []string, target string) int {
	for i, x := range xs {
		if x == target {
			return i
		}
	}
	return -1
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
