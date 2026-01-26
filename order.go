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
	// Dataset de référence pour tous les points d'arrêts (format spécifique fourni)
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
	if err := json.NewDecoder(respStops.Body).Decode(&rawStops); err != nil {
		fmt.Printf("❌ Erreur décodage stops: %v\n", err)
		return
	}

	stopLookup := make(map[string]string)
	for _, s := range rawStops {
		// Utilisation des champs fournis dans ton exemple JSON
		id := fmt.Sprint(s["arrid"])

		var lat, lon float64
		if geo, ok := s["arrgeopoint"].(map[string]interface{}); ok {
			lat, _ = geo["lat"].(float64)
			lon, _ = geo["lon"].(float64)
		}

		if id != "" && id != "<nil>" && lat != 0 {
			// On utilise 4 décimales (~11m de précision) pour le matching flou
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
	if err := json.NewDecoder(respTraces.Body).Decode(&rawTraces); err != nil {
		fmt.Printf("❌ Erreur décodage traces: %v\n", err)
		return
	}

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

		// Navigation sécurisée dans l'objet Shape (GeoJSON)
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

		// On parcourt les segments (MultiLineString)
		for _, segment := range coordsSegments {
			points, ok := segment.([]interface{})
			if !ok {
				continue
			}

			var currentVariant []string
			var lastAdded string

			for _, p := range points {
				coord, ok := p.([]interface{})
				if !ok || len(coord) < 2 {
					continue
				}

				// GeoJSON : [lon, lat]
				ln := coord[0].(float64)
				lt := coord[1].(float64)

				// Tentative de matching sur la coordonnée précise (4 décimales)
				key := fmt.Sprintf("%.4f,%.4f", lt, ln)
				if stopID, found := stopLookup[key]; found {
					if stopID != lastAdded {
						currentVariant = append(currentVariant, stopID)
						lastAdded = stopID
					}
				}
			}

			// On n'enregistre que si on a trouvé au moins 2 arrêts sur le tracé
			if len(currentVariant) >= 2 && !isDuplicate(tempMap[routeID].Variants, currentVariant) {
				tempMap[routeID].Variants = append(tempMap[routeID].Variants, currentVariant)
			}
		}
	}

	// 3. Conversion de la Map en tableau plat []OptimizedLine
	var final []OptimizedLine
	for _, v := range tempMap {
		final = append(final, *v)
	}

	// Sauvegarde finale
	data, _ := json.MarshalIndent(final, "", "  ")
	os.WriteFile("optimized_routes.json", data, 0644)
	fmt.Printf("✅ %d lignes traitées et sauvegardées dans optimized_routes.json\n", len(final))
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
