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
	ID    string   `json:"id"`    // Format: {route_id}_{index}
	Stops []string `json:"stops"` // Liste des IDs d'arrêts
}

type OptimizedLine struct {
	ID        int       `json:"id"`       // ID numérique unique (1, 2, 3...)
	RouteID   string    `json:"route_id"` // ID technique IDFM
	ShortName string    `json:"short_name"`
	Variants  []Variant `json:"variants"`
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
	if err := json.NewDecoder(respStops.Body).Decode(&rawStops); err != nil {
		checkErrOrder(err, RoutesFile, RoutesBackup)
		return
	}

	stopLookup := make(map[string]string)
	for _, s := range rawStops {
		stopID := fmt.Sprint(s["stop_id"])
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

	// --- NOUVEAU : Tri des clés pour garantir des IDs stables ---
	var routeIDs []string
	for rID := range rawVariantsMap {
		routeIDs = append(routeIDs, rID)
	}
	sort.Strings(routeIDs) // Tri par ordre alphabétique des IDs IDFM

	var finalData []OptimizedLine
	idCounter := 1 // Initialisation de l'ID numérique

	for _, routeID := range routeIDs {
		variants := rawVariantsMap[routeID]

		sort.Slice(variants, func(i, j int) bool {
			return len(variants[i]) > len(variants[j])
		})

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

		// Ajout de l'ID numérique incrémenté
		finalData = append(finalData, OptimizedLine{
			ID:        idCounter,
			RouteID:   routeID,
			ShortName: lineNames[routeID],
			Variants:  variantObjects,
		})
		idCounter++
	}

	data, _ := json.MarshalIndent(finalData, "", "  ")
	if err := os.WriteFile(RoutesFile, data, 0644); err != nil {
		checkErrOrder(err, RoutesFile, RoutesBackup)
		return
	}
	fmt.Printf("✅ %d lignes traitées. Fichier généré : %s\n", len(finalData), RoutesFile)
}

// ... (Gardez les fonctions utilitaires : prepareBackupOrder, checkErrOrder, isSubSequence, isDuplicate, hashSequence)

// --- Fonctions Utilitaires Système (Backup & Error Handling) ---

func prepareBackupOrder(file string, backup string) {
	if _, err := os.Stat(file); err == nil {
		input, _ := os.ReadFile(file)
		os.WriteFile(backup, input, 0644)
	}
}

func checkErrOrder(err error, file string, backup string) {
	fmt.Printf("❌ Erreur : %v\n", err)
	if _, statErr := os.Stat(backup); statErr == nil {
		fmt.Println("🔄 Restauration du fichier depuis le backup...")
		input, _ := os.ReadFile(backup)
		os.WriteFile(file, input, 0644)
	} else {
		fmt.Println("⚠️ Aucun backup disponible pour restauration.")
	}
}

// --- Fonctions de Logique Métier (Filtrage) ---

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
