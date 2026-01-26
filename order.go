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

// URLs des Datasets IDFM
const (
	StopsIDFMURL = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/arrets-lignes/exports/json?limit=-1"
	TracesURL    = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/traces-des-lignes-de-transport-en-commun-idfm/exports/json?limit=-1"
)

type OptimizedLine struct {
	RouteID   string     `json:"route_id"`
	ShortName string     `json:"short_name"`
	Variants  [][]string `json:"variants"`
}

func FetchRoutes() {
	// 1. Récupération des arrêts pour le matching géographique
	fmt.Println("⏳ Fetching stops from IDFM...")
	respStops, err := http.Get(StopsIDFMURL)
	if err != nil {
		fmt.Printf("❌ Erreur stops: %v\n", err)
		return
	}
	defer respStops.Body.Close()

	var rawStops []map[string]interface{}
	if err := json.NewDecoder(respStops.Body).Decode(&rawStops); err != nil {
		fmt.Printf("❌ Erreur décodage stops: %v\n", err)
		return
	}

	// Indexation : on utilise une précision de 4 décimales (~10m) pour être plus tolérant
	stopLookup := make(map[string]string)
	for _, s := range rawStops {
		id := fmt.Sprint(s["id_arret"])
		lat, ok1 := s["stop_lat"].(float64)
		lon, ok2 := s["stop_lon"].(float64)

		if ok1 && ok2 && id != "" && id != "<nil>" {
			key := fmt.Sprintf("%.4f,%.4f", lat, lon)
			stopLookup[key] = id
		}
	}
	fmt.Printf("✅ %d stops indexés\n", len(stopLookup))

	// 2. Récupération des traces géométriques
	fmt.Println("⏳ Fetching traces from IDFM...")
	respTraces, err := http.Get(TracesURL)
	if err != nil {
		fmt.Printf("❌ Erreur traces: %v\n", err)
		return
	}
	defer respTraces.Body.Close()

	var rawTraces []map[string]interface{}
	if err := json.NewDecoder(respTraces.Body).Decode(&rawTraces); err != nil {
		fmt.Printf("❌ Erreur décodage traces: %v\n", err)
		return
	}

	// Utilisation d'une map temporaire pour grouper par RouteID
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

		// Navigation dans l'objet Shape (GeoJSON MultiLineString)
		shape, ok := item["shape"].(map[string]interface{})
		if !ok {
			continue
		}
		geometry, ok := shape["geometry"].(map[string]interface{})
		if !ok {
			continue
		}
		coordsSegments, ok := geometry["coordinates"].([]interface{})
		if !ok {
			continue
		}

		// Pour chaque segment de la ligne
		for _, segment := range coordsSegments {
			points, ok := segment.([]interface{})
			if !ok {
				continue
			}

			var currentVariant []string
			var lastAddedStopID string

			for _, p := range points {
				pCoord, ok := p.([]interface{})
				if !ok || len(pCoord) < 2 {
					continue
				}

				lon := pCoord[0].(float64)
				lat := pCoord[1].(float64)

				// Matching avec 4 décimales
				key := fmt.Sprintf("%.4f,%.4f", lat, lon)
				if stopID, found := stopLookup[key]; found {
					if stopID != lastAddedStopID {
						currentVariant = append(currentVariant, stopID)
						lastAddedStopID = stopID
					}
				}
			}

			// On n'ajoute que si la séquence a au moins 2 arrêts et n'est pas un doublon
			if len(currentVariant) >= 2 && !isDuplicate(tempMap[routeID].Variants, currentVariant) {
				tempMap[routeID].Variants = append(tempMap[routeID].Variants, currentVariant)
			}
		}
	}

	// 3. Transformation de la Map en Tableau (Slice)
	var finalTable []OptimizedLine
	for _, val := range tempMap {
		finalTable = append(finalTable, *val)
	}

	// 4. Sauvegarde
	output, _ := json.MarshalIndent(finalTable, "", "  ")
	os.WriteFile("optimized_routes.json", output, 0644)
	fmt.Printf("✅ %d lignes traitées et sauvegardées dans optimized_routes.json\n", len(finalTable))
}

func isDuplicate(existing [][]string, newVariant []string) bool {
	newHash := hashSequence(newVariant)
	for _, v := range existing {
		if hashSequence(v) == newHash {
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
