package main

import (
	"context"
	"fmt"
	"github.com/A2gent/brute/internal/tools"
)

func main() {
	grep := tools.NewGrepTool(".")
	res, err := grep.Execute(context.Background(), []byte(`{"pattern": "FindFilesTool", "include": "*.go"}`))
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Println("Success:", res.Success)
	fmt.Println("Output:\n", res.Output)
}
