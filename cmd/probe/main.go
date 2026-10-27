package main

import (
	"encoding/json"
	"fmt"

	"cpa-today-plugin/internal/app"
)

func main() {
	a := app.New("0.1.0")
	raw, err := a.HandleMethod("plugin.register", nil)
	if err != nil {
		fmt.Println("ERR:", err)
		return
	}
	var pretty any
	_ = json.Unmarshal(raw, &pretty)
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println(string(out))
}
