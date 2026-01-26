package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	// On utilise arrets-lignes pour avoir les IDs exacts de ta DB (ex: IDFM:423541)
	StopsLignesURL = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/arrets-lignes/exports/json?limit=-1"
	TracesURL      = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/traces-des-lignes-de-transport-en-commun-idfm/exports/json?limit=-1"
)

type OptimizedLine struct {
	RouteID   string     `json:"route_id"`
	ShortName string     `json:"short_name"`
	Variants  [][]string `json:"variants"`
}

func FetchRoutes() {
	// 1. Indexation des Arrêts depuis arrets-lignes
	fmt.Println("⏳ Récupération des arrêts-lignes depuis IDFM...")
	respStops, err := http.Get(StopsLignesURL)
	if err != nil {
		fmt.Printf("❌ Erreur HTTP Stops: %v\n", err)
		return
	}
	defer respStops.Body.Close()

	var rawStops []map[string]interface{}
	if err := json.NewDecoder(respStops.Body).Decode(&rawStops); err != nil {
		fmt.Printf("❌ Erreur décodage stops: %v\n", err)
		return
	}

	stopLookup := make(map[string]string)
	for _, s := range rawStops {
		// On récupère le stop_id (ex: IDFM:423541)
		stopID := fmt.Sprint(s["stop_id"])

		var lat, lon float64
		// On privilégie l'objet pointgeo fourni dans ton exemple
		if geo, ok := s["pointgeo"].(map[string]interface{}); ok {
			lat, _ = geo["lat"].(float64)
			lon, _ = geo["lon"].(float64)
		} else {
			// Fallback sur stop_lat/stop_lon (parfois en string dans ce dataset)
			lat, _ = strconv.ParseFloat(fmt.Sprint(s["stop_lat"]), 64)
			lon, _ = strconv.ParseFloat(fmt.Sprint(s["stop_lon"]), 64)
		}

		if stopID != "" && stopID != "<nil>" && lat != 0 {
			// Matching flou à 4 décimales (~10-11 mètres)
			key := fmt.Sprintf("%.4f,%.4f", lat, lon)
			stopLookup[key] = stopID
		}
	}
	fmt.Printf("✅ %d arrêts indexés (format stop_id IDFM)\n", len(stopLookup))

	// 2. Traitement des Tracés
	fmt.Println("⏳ Récupération des tracés depuis IDFM...")
	respTraces, err := http.Get(TracesURL)
	if err != nil {
		fmt.Printf("❌ Erreur HTTP Traces: %v\n", err)
		return
	}
	defer respTraces.Body.Close()

	var rawTraces []map[string]interface{}
	if err := json.NewDecoder(respTraces.Body).Decode(&rawTraces); err != nil {
		fmt.Printf("❌ Erreur décodage traces: %v\n", err)
		return
	}

	tempMap := make(map[string]*OptimizedLine)

	for _, item := range rawTraces {
		// id_ilico dans les traces correspond à l'id de ligne dans arrets-lignes (ex: IDFM:C02708)
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

		shape, _ := item["shape"].(map[string]interface{})
		if shape == nil {
			continue
		}
		geometry, _ := shape["geometry"].(map[string]interface{})
		if geometry == nil {
			continue
		}

		coordsSegments, ok := geometry["coordinates"].([]interface{})
		if !ok {
			continue
		}

		for _, segment := range coordsSegments {
			points, ok := segment.([]interface{})
			if !ok {
				continue
			}

			var currentVariant []string
			var lastAddedID string

			for _, p := range points {
				coord, ok := p.([]interface{})
				if !ok || len(coord) < 2 {
					continue
				}

				ln := coord[0].(float64)
				lt := coord[1].(float64)

				key := fmt.Sprintf("%.4f,%.4f", lt, ln)
				if stopID, found := stopLookup[key]; found {
					if stopID != lastAddedID {
						currentVariant = append(currentVariant, stopID)
						lastAddedID = stopID
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

	// Sauvegarde
	data, _ := json.MarshalIndent(final, "", "  ")
	os.WriteFile("optimized_routes.json", data, 0644)
	fmt.Printf("✅ %d lignes traitées avec succès.\n", len(final))
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
