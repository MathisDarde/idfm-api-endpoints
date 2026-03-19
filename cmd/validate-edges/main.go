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
	Variants              []Variant   `json:"variants"`
	OptimalInfrastructure [][]string `json:"optimal_infrastructure"`
}

type Variant struct {
	ID    string   `json:"id"`
	Stops []string `json:"stops"`
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
		{
			RouteID: "IDFM:C01742",
			A:       "IDFM:monomodalStopPlace:43172", // Neuilly-Plaisance
			B:       "IDFM:monomodalStopPlace:47238", // Fontenay-sous-Bois
			Label:   "Neuilly-Plaisance ↔ Fontenay-sous-Bois (direct)",
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
		if c.Label == "Neuilly-Plaisance ↔ Fontenay-sous-Bois (direct)" {
			neighbors := commonNeighbors(r.OptimalInfrastructure, c.A, c.B)
			if len(neighbors) > 0 {
				fmt.Printf("  common neighbors (%d):\n", len(neighbors))
				for _, n := range neighbors {
					fmt.Printf("  - %s\n", displayStop(stopsByID, n))
				}
			}
		}
	}

	// Branch-mixing checks for RER A.
	rerA, ok := routesByID["IDFM:C01742"]
	if ok {
		exitCode = max(exitCode, validateRerABranches(stopsByID, rerA))
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

func commonNeighbors(edges [][]string, a, b string) []string {
	adj := make(map[string]map[string]bool)
	add := func(x, y string) {
		m, ok := adj[x]
		if !ok {
			m = make(map[string]bool)
			adj[x] = m
		}
		m[y] = true
	}
	for _, e := range edges {
		if len(e) != 2 {
			continue
		}
		add(e[0], e[1])
		add(e[1], e[0])
	}

	na := adj[a]
	nb := adj[b]
	if na == nil || nb == nil {
		return nil
	}

	var common []string
	for n := range na {
		if nb[n] {
			common = append(common, n)
		}
	}
	return common
}

func validateRerABranches(stopsByID map[string]Stop, rerA OptimizedLine) int {
	// West branch rules (service patterns): no single variant should mix Poissy terminus
	// with Cergy-only stops.
	// IDs from stops.json:
	poissy := "IDFM:monomodalStopPlace:47874"
	acheresVille := "IDFM:monomodalStopPlace:46647"
	acheresGrandCormier := "IDFM:monomodalStopPlace:47915"
	conflans := "IDFM:monomodalStopPlace:43114"
	cergyPref := "IDFM:monomodalStopPlace:44559"
	neuville := "IDFM:monomodalStopPlace:47879"
	cergyStChris := "IDFM:monomodalStopPlace:47897"

	exitCode := 0
	for _, v := range rerA.Variants {
		set := make(map[string]bool, len(v.Stops))
		for _, s := range v.Stops {
			set[s] = true
		}

		if set[poissy] {
			// Poissy branch must include Achères Ville.
			if !set[acheresVille] {
				fmt.Printf("[FAIL] variant %s contains Poissy but not Achères Ville\n", v.ID)
				exitCode = 2
			}
			// Poissy variants must not include Cergy-only stops.
			for _, bad := range []string{conflans, cergyPref, neuville, cergyStChris} {
				if set[bad] {
					fmt.Printf("[FAIL] variant %s mixes Poissy with %s\n", v.ID, displayStop(stopsByID, bad))
					exitCode = 2
				}
			}
			// Poissy must not be directly adjacent to Achères Grand Cormier.
			if set[acheresGrandCormier] && areAdjacent(v.Stops, poissy, acheresGrandCormier) {
				fmt.Printf("[FAIL] variant %s has Poissy adjacent to %s\n", v.ID, displayStop(stopsByID, acheresGrandCormier))
				exitCode = 2
			}
		}

		// Also forbid the reverse: Cergy-side variants containing Poissy.
		if set[cergyPref] || set[conflans] || set[neuville] || set[cergyStChris] {
			if set[poissy] {
				fmt.Printf("[FAIL] variant %s mixes Cergy-side with Poissy\n", v.ID)
				exitCode = 2
			}
		}
	}
	return exitCode
}

func areAdjacent(stops []string, a, b string) bool {
	for i := 0; i < len(stops)-1; i++ {
		if (stops[i] == a && stops[i+1] == b) || (stops[i] == b && stops[i+1] == a) {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
