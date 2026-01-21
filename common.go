package main

import (
	"fmt"
	"os"
)

// Gère le backup d'un fichier avant écrasement
func prepareBackup(filename string, backupName string) {
	if _, err := os.Stat(filename); err == nil {
		_ = os.Rename(filename, backupName)
	}
}

// En cas d'erreur, restaure le backup
func restoreBackup(filename string, backupName string) {
	if _, err := os.Stat(backupName); err == nil {
		fmt.Printf("🔄 Restauration du backup pour %s\n", filename)
		_ = os.Rename(backupName, filename)
	}
}

// Aide pour gérer les erreurs de manière centralisée
func checkErr(err error, filename string, backupName string) {
	if err != nil {
		fmt.Printf("❌ Erreur sur %s: %v\n", filename, err)
		restoreBackup(filename, backupName)
		// On ne fait pas os.Exit(1) ici pour laisser les autres scripts (ex: lines) tourner
	}
}
