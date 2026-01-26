package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	// Dataset de référence pour tous les points d'arrêts (plus fiable)
	StopsIDFMURL = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/arrets/exports/json?limit=-1"
	TracesURL    = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/traces-des-lignes-de-transport-en-commun-idfm/exports/json?limit=-1"
)

type OptimizedLine struct {
	RouteID   string     `json:"route_id"`
	ShortName string     `json:"short_name"`
	Variants  [][]string `json:"variants"`
}

func FetchRoutes() {
	// 1. Indexation des Arrêts
	fmt.Println("⏳ Récupération des arrêts depuis IDFM...")
	respStops, err := http.Get(StopsIDFMURL)
	if err != nil {
		fmt.Printf("❌ Erreur HTTP Stops: %v\n", err)
		return
	}
	defer respStops.Body.Close()

	var rawStops []map[string]interface{}
	json.NewDecoder(respStops.Body).Decode(&rawStops)

	stopLookup := make(map[string]string)
	for _, s := range rawStops {
		// IDFM utilise souvent 'stop_id' ou 'id' ou 'arret_id'
		id := getFirstString(s, "stop_id", "id", "arret_id", "id_arret")

		var lat, lon float64
		// Tentative via les champs directs
		if l, ok := s["stop_lat"].(float64); ok {
			lat = l
			lon = s["stop_lon"].(float64)
		} else if geo, ok := s["geo_point_2d"].(map[string]interface{}); ok {
			// Tentative via l'objet geo_point_2d (standard IDFM)
			lat = geo["lat"].(float64)
			lon = geo["lon"].(float64)
		}

		if id != "" && lat != 0 {
			// On utilise 4 décimales (~11 mètres de précision) pour le matching
			key := fmt.Sprintf("%.4f,%.4f", lat, lon)
			stopLookup[key] = id
		}
	}
	fmt.Printf("✅ %d arrêts indexés pour le matching\n", len(stopLookup))

	// 2. Traitement des Tracés
	fmt.Println("⏳ Récupération des tracés depuis IDFM...")
	respTraces, err := http.Get(TracesURL)
	if err != nil {
		fmt.Printf("❌ Erreur HTTP Traces: %v\n", err)
		return
	}
	defer respTraces.Body.Close()

	var rawTraces []map[string]interface{}
	json.NewDecoder(respTraces.Body).Decode(&rawTraces)

	tempMap := make(map[string]*OptimizedLine)

	for _, item := range rawTraces {
		routeID := fmt.Sprint(item["id_ilico"])
		shortName := fmt.Sprint(item["route_short_name"])
		if routeID == "" || routeID == "<nil>" {
			continue
		}

		if _, ok := tempMap[routeID]; !ok {
			tempMap[routeID] = &OptimizedLine{
				RouteID:   routeID,
				ShortName: shortName,
				Variants:  [][]string{},
			}
		}

		// Extraction GeoJSON
		shape, _ := item["shape"].(map[string]interface{})
		geometry, _ := shape["geometry"].(map[string]interface{})
		coordsSegments, ok := geometry["coordinates"].([]interface{})
		if !ok {
			continue
		}

		// On gère MultiLineString (IDFM)
		for _, segment := range coordsSegments {
			points := segment.([]interface{})
			var currentVariant []string
			var lastAdded string

			for _, p := range points {
				coord := p.([]interface{})
				ln, lt := coord[0].(float64), coord[1].(float64)

				// Matching flou à 4 décimales
				key := fmt.Sprintf("%.4f,%.4f", lt, ln)
				if stopID, found := stopLookup[key]; found {
					if stopID != lastAdded {
						currentVariant = append(currentVariant, stopID)
						lastAdded = stopID
					}
				}
			}

			if len(currentVariant) >= 2 && !isDuplicate(tempMap[routeID].Variants, currentVariant) {
				tempMap[routeID].Variants = append(tempMap[routeID].Variants, currentVariant)
			}
		}
	}

	// 3. Conversion en tableau final
	var final []OptimizedLine
	for _, v := range tempMap {
		final = append(final, *v)
	}

	data, _ := json.MarshalIndent(final, "", "  ")
	os.WriteFile("optimized_routes.json", data, 0644)
	fmt.Printf("✅ %d lignes traitées avec variants.\n", len(final))
}

// Helper pour tester plusieurs noms de champs JSON possibles
func getFirstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if val, ok := m[k]; ok && val != nil {
			return fmt.Sprint(val)
		}
	}
	return ""
}

func isDuplicate(existing [][]string, newVar []string) bool {
	h := hashSequence(newVar)
	for _, e := range existing {
		if hashSequence(e) == h {
			return true
		}
	}
	return false
}

func hashSequence(s []string) string {
	hasher := md5.New()
	io.WriteString(hasher, strings.Join(s, ","))
	return fmt.Sprintf("%x", hasher.Sum(nil))
}
