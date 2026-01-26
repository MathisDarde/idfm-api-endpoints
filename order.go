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
	TracesURL = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/traces-des-lignes-de-transport-en-commun-idfm/exports/json?limit=-1"
)

type OptimizedLine struct {
	RouteID  string     `json:"route_id"`
	Variants [][]string `json:"variants"` // Listes d'IDs de stops
}

func FetchRoutes() {
	// 1. Récupération des arrêts pour le matching géographique
	fmt.Println("⏳ Fetching stops from IDFM...")
	respStops, err := http.Get(StopsURL)
	if err != nil {
		fmt.Printf("❌ Erreur stops: %v\n", err)
		return
	}
	defer respStops.Body.Close()

	var rawStops []map[string]interface{}
	json.NewDecoder(respStops.Body).Decode(&rawStops)

	stopLookup := make(map[string]string)
	for _, s := range rawStops {
		id := fmt.Sprint(s["id_arret"])
		lat, ok1 := s["stop_lat"].(float64)
		lon, ok2 := s["stop_lon"].(float64)

		if ok1 && ok2 && id != "" && id != "<nil>" {
			key := fmt.Sprintf("%.5f,%.5f", lat, lon)
			stopLookup[key] = id
		}
	}
	fmt.Printf("✅ %d stops indexés pour le matching\n", len(stopLookup))

	// 2. Récupération des traces géométriques
	fmt.Println("⏳ Fetching traces from IDFM...")
	respTraces, err := http.Get(TracesURL)
	if err != nil {
		fmt.Printf("❌ Erreur traces: %v\n", err)
		return
	}
	defer respTraces.Body.Close()

	var rawTraces []map[string]interface{}
	json.NewDecoder(respTraces.Body).Decode(&rawTraces)

	finalResult := make(map[string]*OptimizedLine)

	for _, item := range rawTraces {
		// On utilise id_ilico qui correspond à l'ID de ligne standard
		routeID := fmt.Sprint(item["id_ilico"])
		if routeID == "" || routeID == "<nil>" {
			continue
		}

		if _, ok := finalResult[routeID]; !ok {
			finalResult[routeID] = &OptimizedLine{
				RouteID:  routeID,
				Variants: [][]string{},
			}
		}

		// Extraction de la géométrie MultiLineString
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

		for _, segment := range coordsSegments {
			points := segment.([]interface{})
			var currentVariant []string
			var lastStopID string

			for _, p := range points {
				pCoord := p.([]interface{})
				lon := pCoord[0].(float64)
				lat := pCoord[1].(float64)

				// Matching via la clé "lat,lon" arrondie
				key := fmt.Sprintf("%.5f,%.5f", lat, lon)
				if stopID, found := stopLookup[key]; found {
					// Évite les doublons consécutifs (ex: un bus qui reste à l'arrêt)
					if stopID != lastStopID {
						currentVariant = append(currentVariant, stopID)
						lastStopID = stopID
					}
				}
			}

			// On n'ajoute que si la séquence est nouvelle pour cette ligne
			if len(currentVariant) >= 2 && !isDuplicate(finalResult[routeID].Variants, currentVariant) {
				finalResult[routeID].Variants = append(finalResult[routeID].Variants, currentVariant)
			}
		}
	}

	// 3. Sauvegarde du résultat
	output, _ := json.MarshalIndent(finalResult, "", "  ")
	os.WriteFile("optimized_routes.json", output, 0644)
	fmt.Printf("✅ %d lignes traitées et sauvegardées dans optimized_routes.json\n", len(finalResult))
}

// isDuplicate vérifie si la séquence d'arrêts existe déjà via un hash MD5
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
