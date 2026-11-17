// Command tbmlines turns the TBM: protocol lines in an events file into the record format
// tools/tbmcontract/contract.lua replays through the TortoiseBotsManager addon.
//
// The input is what the events RPC returns, saved to a file: a bare JSON array of events,
// {"events": [...]} or the full {"result": {"events": [...]}} reply. Records come out in
// event order, one per line:
//
//	system<TAB><text>                  a system message (the raw text, with its |r codes)
//	addon<TAB><CHANNEL><TAB><text>     an addon message with prefix TBM
//
// Usage: tbmlines [-o out.txt] events.json...
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/tbm"
)

func main() {
	out := flag.String("o", "", "write the records here instead of stdout")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: tbmlines [-o out.txt] events.json...")
		os.Exit(2)
	}
	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tbmlines:", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	total := 0
	for _, path := range flag.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tbmlines:", err)
			os.Exit(1)
		}
		evs, err := decode(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tbmlines: %s: %v\n", path, err)
			os.Exit(1)
		}
		n, err := Write(bw, evs)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tbmlines:", err)
			os.Exit(1)
		}
		total += n
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "tbmlines:", err)
		os.Exit(1)
	}
	if total == 0 {
		fmt.Fprintln(os.Stderr, "tbmlines: no TBM: lines in the input")
		os.Exit(1)
	}
}

// decode accepts the three shapes an events reply is saved in.
func decode(data []byte) ([]events.Event, error) {
	var list []events.Event
	if err := json.Unmarshal(data, &list); err == nil {
		return list, nil
	}
	var obj struct {
		Events []events.Event `json:"events"`
		Result *struct {
			Events []events.Event `json:"events"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("not an events file: %w", err)
	}
	if obj.Result != nil {
		return obj.Result.Events, nil
	}
	return obj.Events, nil
}

// Write emits one record per TBM: line in evs and returns how many it wrote.
func Write(w io.Writer, evs []events.Event) (int, error) {
	n := 0
	for _, e := range evs {
		line, ok := tbm.Line(e)
		if !ok {
			continue
		}
		var rec string
		if e.Kind == events.KindAddon {
			ch, _ := e.Data["type"].(string)
			if ch == "" {
				ch = "unknown"
			}
			rec = "addon\t" + strings.ToUpper(ch) + "\t" + line
		} else {
			rec = "system\t" + line
		}
		if _, err := fmt.Fprintln(w, rec); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
