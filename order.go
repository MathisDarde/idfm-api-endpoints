package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
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

	stopLookup := make(map[string]string)
	stopNameMap := make(map[string]string) // stop_id → stop_name
	for _, s := range rawStops {
		stopID := fmt.Sprint(s["stop_id"])
		stopName := fmt.Sprint(s["stop_name"])
		var lat, lon float64
		if geo, ok := s["pointgeo"].(map[string]interface{}); ok {
			lat, _ = geo["lat"].(float64)
			lon, _ = geo["lon"].(float64)
		} else {
			lat, _ = strconv.ParseFloat(fmt.Sprint(s["stop_lat"]), 64)
			lon, _ = strconv.ParseFloat(fmt.Sprint(s["stop_lon"]), 64)
		}

		if stopID != "" && stopID != "<nil>" && lat != 0 {
			key := fmt.Sprintf("%.4f,%.4f", lat, lon)
			stopLookup[key] = stopID
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

	for _, item := range rawTraces {
		rawID := fmt.Sprint(item["id_ilico"])
		if rawID == "" || rawID == "<nil>" {
			continue
		}

		routeID := "IDFM:" + rawID
		lineNames[routeID] = fmt.Sprint(item["route_short_name"])

		shape, _ := item["shape"].(map[string]interface{})
		geometry, _ := shape["geometry"].(map[string]interface{})
		coordsSegments, ok := geometry["coordinates"].([]interface{})
		if !ok {
			continue
		}

		for _, segment := range coordsSegments {
			points := segment.([]interface{})
			var currentVariant []string
			var lastID string

			for _, p := range points {
				coord := p.([]interface{})
				key := fmt.Sprintf("%.4f,%.4f", coord[1].(float64), coord[0].(float64))
				if id, found := stopLookup[key]; found {
					if id != lastID {
						currentVariant = append(currentVariant, id)
						lastID = id
					}
				}
			}
			if len(currentVariant) >= 2 {
				rawVariantsMap[routeID] = append(rawVariantsMap[routeID], currentVariant)
			}
		}
	}

	// --- NORMALISATION DES ARRÊTS DE MÉTRO PAR NOM ---
	// Les arrêts de métro existent en double (un par sens) avec des IDs différents
	// mais le même nom. On normalise pour que les deux sens partagent les mêmes IDs.
	linesFileData, _ := os.ReadFile(LinesFile)
	var linesInfo []map[string]interface{}
	json.Unmarshal(linesFileData, &linesInfo)
	metroRouteIDs := make(map[string]bool)
	for _, l := range linesInfo {
		if fmt.Sprint(l["mode"]) == string(TransportModeMetro) {
			metroRouteIDs[fmt.Sprint(l["id"])] = true
		}
	}

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

		var infrastructure [][]string
		for _, seg := range infrastructureMap {
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
