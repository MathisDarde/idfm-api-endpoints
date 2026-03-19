package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type Stop struct {
	ID     string  `json:"id"`
	LineID string  `json:"line_id"`
	Name   string  `json:"name"`
	City   string  `json:"city"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

type OptimizedLine struct {
	RouteID               string     `json:"route_id"`
	ShortName             string     `json:"short_name"`
	OptimalInfrastructure [][]string `json:"optimal_infrastructure"`
}

type PairCheck struct {
	RouteID string
	A       string
	B       string
	Label   string
}

func main() {
	stopsByID := loadStopsByID("stops.json")
	routes := loadOptimizedLines("optimized_routes.json")
	routesByID := make(map[string]OptimizedLine, len(routes))
	for _, r := range routes {
		routesByID[r.RouteID] = r
	}

	checks := []PairCheck{
		{
			RouteID: "IDFM:C01742",
			A:       "IDFM:monomodalStopPlace:43172", // Neuilly-Plaisance
			B:       "IDFM:monomodalStopPlace:47886", // Nogent-sur-Marne
			Label:   "Neuilly-Plaisance ↔ Nogent-sur-Marne",
		},
		{
			RouteID: "IDFM:C01742",
			A:       "IDFM:monomodalStopPlace:58875", // Rueil-Malmaison
			B:       "IDFM:monomodalStopPlace:43082", // Houilles-Carrières-sur-Seine
			Label:   "Rueil-Malmaison ↔ Houilles-Carrières",
		},
	}

	exitCode := 0
	for _, c := range checks {
		r, ok := routesByID[c.RouteID]
		if !ok {
			fmt.Printf("[WARN] route not found: %s\n", c.RouteID)
			continue
		}

		found := hasEdge(r.OptimalInfrastructure, c.A, c.B)
		nameA := displayStop(stopsByID, c.A)
		nameB := displayStop(stopsByID, c.B)
		fmt.Printf("%s (%s): %v\n  - %s\n  - %s\n", c.Label, c.RouteID, found, nameA, nameB)
		if found {
			exitCode = 2
		}
	}

	os.Exit(exitCode)
}

func hasEdge(edges [][]string, a, b string) bool {
	for _, e := range edges {
		if len(e) != 2 {
			continue
		}
		if (e[0] == a && e[1] == b) || (e[0] == b && e[1] == a) {
			return true
		}
	}
	return false
}

func loadStopsByID(path string) map[string]Stop {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read %s: %v\n", path, err)
		return nil
	}
	var stops []Stop
	if err := json.Unmarshal(b, &stops); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse %s: %v\n", path, err)
		return nil
	}
	m := make(map[string]Stop, len(stops))
	for _, s := range stops {
		if s.ID == "" {
			continue
		}
		if _, exists := m[s.ID]; !exists {
			m[s.ID] = s
		}
	}
	return m
}

func loadOptimizedLines(path string) []OptimizedLine {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read %s: %v\n", path, err)
		return nil
	}
	var lines []OptimizedLine
	if err := json.Unmarshal(b, &lines); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse %s: %v\n", path, err)
		return nil
	}
	return lines
}

func displayStop(stopsByID map[string]Stop, id string) string {
	if stopsByID == nil {
		return id
	}
	if s, ok := stopsByID[id]; ok {
		if s.City != "" {
			return fmt.Sprintf("%s (%s)", s.Name, s.City)
		}
		return s.Name
	}
	return id
}
