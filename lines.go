package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type TransportMode string

const (
	TransportModeRer        TransportMode = "rer"
	TransportModeMetro      TransportMode = "metro"
	TransportModeTransilien TransportMode = "transilien"
	TransportModeTer        TransportMode = "ter"
	TransportModeNavette    TransportMode = "navette"
	TransportModeBus        TransportMode = "bus"
	TransportModeCableway   TransportMode = "cableway"
	TransportModeTramway    TransportMode = "tramway"
)

type LineData struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Mode            TransportMode `json:"mode"`
	BackgroundColor string        `json:"background_color"`
	TextColor       string        `json:"text_color"`
}

const (
	LinesURL    = "https://data.iledefrance-mobilites.fr/api/explore/v2.1/catalog/datasets/referentiel-des-lignes/exports/json?limit=-1"
	LinesFile   = "lines.json"
	LinesBackup = "lines.backup.json"
)

func FetchLines() {
	prepareBackup(LinesFile, LinesBackup)

	resp, err := http.Get(LinesURL)
	if err != nil {
		checkErr(err, LinesFile, LinesBackup)
		return
	}
	defer resp.Body.Close()

	var raw []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		checkErr(err, LinesFile, LinesBackup)
		return
	}

	var processed []LineData
	for _, item := range raw {
		// Extraction sécurisée des strings
		tMode := fmt.Sprint(item["transportmode"])
		tSubMode := fmt.Sprint(item["transportsubmode"])

		line := LineData{
			ID:              fmt.Sprint(item["id_line"]),
			Name:            fmt.Sprint(item["name_line"]),
			Mode:            detectMode(tMode, tSubMode),
			BackgroundColor: fmt.Sprint(item["colourweb_hexa"]),
			TextColor:       fmt.Sprint(item["textcolourweb_hexa"]),
		}

		// Optionnel : ne pas ajouter si l'ID est vide
		if line.ID != "<nil>" && line.ID != "" {
			processed = append(processed, line)
		}
	}

	data, _ := json.MarshalIndent(processed, "", "  ")
	os.WriteFile(LinesFile, data, 0644)
	fmt.Printf("✅ %d lignes traitées\n", len(processed))
}

func detectMode(transportMode string, transportSubmode string) TransportMode {
	// Normalisation en minuscules pour éviter les surprises
	switch transportMode {
	case "rail":
		switch transportSubmode {
		case "suburbanRailway":
			return TransportModeTransilien
		case "local":
			return TransportModeRer
		case "regionalRail":
			return TransportModeTer
		case "railShuttle":
			return TransportModeNavette
		default:
			return TransportModeTransilien
		}
	case "metro":
		return TransportModeMetro
	case "tramway":
		return TransportModeTramway
	case "bus":
		return TransportModeBus
	case "cableway":
		return TransportModeCableway
	default:
		return TransportMode(transportMode)
	}
}
