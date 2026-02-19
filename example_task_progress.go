package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/A2gent/brute/internal/tools"
)

// MockTaskProgressStore for testing
type MockTaskProgressStore struct {
	data map[string]string
}

func (m *MockTaskProgressStore) GetSessionTaskProgress(sessionID string) (string, error) {
	content, exists := m.data[sessionID]
	if !exists {
		return "", nil
	}
	return content, nil
}

func (m *MockTaskProgressStore) SetSessionTaskProgress(sessionID string, progress string) error {
	if m.data == nil {
		m.data = make(map[string]string)
	}
	m.data[sessionID] = progress
	return nil
}

func main() {
	// Create mock store
	store := &MockTaskProgressStore{}

	// Create the tool
	tool := tools.NewSessionTaskProgressTool(store)

	// Create context with session_id
	ctx := context.WithValue(context.Background(), "session_id", "test-session-123")

	// Example 1: Set initial task progress
	setParams, _ := json.Marshal(map[string]interface{}{
		"action": "set",
		"content": `[ ] Step 1: Analyze requirements
  [ ] Step 1.1: Read project files
  [x] Step 1.2: Identify patterns
[ ] Step 2: Implement solution
  [ ] Step 2.1: Write code
  [ ] Step 2.2: Add tests
[x] Step 3: Documentation
  [x] Step 3.1: Write README
  [ ] Step 3.2: Add comments`,
	})

	result, err := tool.Execute(ctx, setParams)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Set result:", result.Output)
	fmt.Printf("Metadata: %+v\n", result.Metadata)

	// Example 2: Get current progress
	getParams, _ := json.Marshal(map[string]interface{}{
		"action": "get",
	})

	result, err = tool.Execute(ctx, getParams)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nCurrent progress:")
	fmt.Println(result.Output)

	// Example 3: Append new tasks
	appendParams, _ := json.Marshal(map[string]interface{}{
		"action": "append",
		"content": `[ ] Step 4: Deployment
  [ ] Step 4.1: Setup CI/CD
  [ ] Step 4.2: Deploy to production`,
	})

	result, err = tool.Execute(ctx, appendParams)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nAppend result:", result.Output)

	// Example 4: Update progress (mark tasks as completed)
	updateParams, _ := json.Marshal(map[string]interface{}{
		"action": "set",
		"content": `[x] Step 1: Analyze requirements
  [x] Step 1.1: Read project files
  [x] Step 1.2: Identify patterns
[x] Step 2: Implement solution
  [x] Step 2.1: Write code
  [ ] Step 2.2: Add tests
[x] Step 3: Documentation
  [x] Step 3.1: Write README
  [x] Step 3.2: Add comments
[ ] Step 4: Deployment
  [ ] Step 4.1: Setup CI/CD
  [ ] Step 4.2: Deploy to production`,
	})

	result, err = tool.Execute(ctx, updateParams)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nUpdated progress:", result.Output)
	fmt.Printf("Final metadata: %+v\n", result.Metadata)
}