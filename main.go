package main

import "fmt"

func main() {
	fmt.Println("🚦 Début de la mise à jour des données IDFM")

	fmt.Println("📍 Traitement des arrêts...")
	FetchStops()

	fmt.Println("✅ Toutes les tâches sont terminées.")
}
