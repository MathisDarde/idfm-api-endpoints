package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// Structure simplifiée pour vos données finales
type StopData struct {
	ID     string
	LineID string
	Name   string
	City   string
	geom   interface{}
}

const (
	// Exemple : URL d'export CSV/JSON du référentiel des arrêts (Open Data IDFM)
	// Remplacez par le dataset spécifique dont vous avez besoin
	StopsURL    = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/arrets-lignes/exports/json?limit=-1"
	StopsFile   = "stops.json"
	StopsBackup = "stops.backup.json"
)

func FetchStops() {
	prepareBackup(StopsFile, StopsBackup)

	resp, err := http.Get(StopsURL)
	if err != nil {
		checkErr(err, StopsFile, StopsBackup)
		return
	}
	defer resp.Body.Close()

	var raw []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		checkErr(err, StopsFile, StopsBackup)
		return
	}

	var processed []map[string]interface{}
	for _, item := range raw {
		var lat, lon float64
		if geo, ok := item["pointgeo"].(map[string]interface{}); ok {
			lat, _ = geo["lat"].(float64)
			lon, _ = geo["lon"].(float64)
		}

		processed = append(processed, map[string]interface{}{
			"id":      fmt.Sprint(item["stop_id"]),
			"line_id": fmt.Sprint(item["id"]),
			"name":    fmt.Sprint(item["stop_name"]),
			"city":    fmt.Sprint(item["nom_commune"]),
			"lat":     lat,
			"lon":     lon,
		})
	}

	data, _ := json.MarshalIndent(processed, "", "  ")
	os.WriteFile(StopsFile, data, 0644)
}
