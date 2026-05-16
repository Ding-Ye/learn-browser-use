package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// s05-element-actor demo binary.
//
// What it does:
//  1. Builds a RecordingCDPClient + an Element with NodeID=42.
//  2. Calls Click (with Shift+Ctrl modifiers), Type ("hello 你好 👋"),
//     and Screenshot back-to-back.
//  3. Prints every recorded frame as JSON, in dispatch order.
//
// The point of the demo is to make the "CDP wire" visible: if you were
// hooked up to real Chromium, these exact frames would be what your
// agent sent. Swapping the recorder for a real WebSocket client is the
// only change s07 needs to make.
//
// Run: go run .
func main() {
	ctx := context.Background()

	client := NewRecordingCDPClient()
	el := Element{Client: client, NodeID: 42}

	if err := el.Click(ctx, ClickOptions{
		Button:     "left",
		ClickCount: 1,
		Modifiers:  []string{"Shift", "Control"},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "click: %v\n", err)
		os.Exit(1)
	}

	if err := el.Type(ctx, "hello 你好 👋"); err != nil {
		fmt.Fprintf(os.Stderr, "type: %v\n", err)
		os.Exit(1)
	}

	png, err := el.Screenshot(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "screenshot: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("# recorded CDP frames")
	fmt.Println()
	for i, f := range client.Frames {
		pretty, err := prettyFrame(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "render frame %d: %v\n", i, err)
			os.Exit(1)
		}
		fmt.Printf("[%d] %s\n", i, pretty)
		fmt.Println()
	}

	fmt.Printf("# screenshot bytes: %d total, header=%x\n", len(png), png[:min(8, len(png))])
}

// prettyFrame renders a Frame as two lines of JSON-ish text:
//
//	Method.Name
//	  { ... params ... }
//
// We sort the params map by re-marshalling through encoding/json so
// the output is deterministic — Go's map iteration is randomised, and
// without this the testdata golden would flake every run.
func prettyFrame(f Frame) (string, error) {
	body, err := json.MarshalIndent(f.Params, "  ", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\n  %s", f.Method, string(body)), nil
}
