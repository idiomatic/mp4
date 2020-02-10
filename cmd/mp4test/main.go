package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/idiomatic/mp4"
)

// Dump displays atom metadata while traversing.
func Dump(rd io.Reader) error {
	return mp4.Walk(rd, func(ancestors []*mp4.Atom, a *mp4.Atom) error {
		fmt.Printf("%v %s (%d + %d)\n", mp4.TypePath(ancestors), a.Type, a.Start, a.Size)
		return nil
	})
}

// CopyCover copies cover art.
func CopyCover(rd io.Reader, wr io.Writer) error {
	return mp4.Walk(rd, func(_ []*mp4.Atom, a *mp4.Atom) error {
		if string(a.Type) == "covr" {
			return a.Walk(func(_ []*mp4.Atom, a *mp4.Atom) error {
				_, err := io.Copy(wr, a)
				return err
			})
		}
		return nil
	})
}

func dumpJSON(data interface{}) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(data); err != nil {
		return err
	}
	return nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "not enough arguments")
		os.Exit(1)
	}

	rd, err := os.Open(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rd.Close()

	switch os.Args[1] {
	case "dump":
		if err := Dump(rd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "itunes":
		collector := mp4.ITunesMetadata{}
		if err := collector.Read(rd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := dumpJSON(collector); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "mvhd":
		collector := mp4.MVHD{}
		if err := collector.Read(rd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := dumpJSON(collector); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "tkhd":
		collector := mp4.TKHD{}
		if err := collector.Read(rd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := dumpJSON(collector); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "cover":
		wr := os.Stdout
		if len(os.Args) > 2 {
			wr, err = os.OpenFile(os.Args[2], os.O_WRONLY|os.O_CREATE, 0755)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}

		if err := CopyCover(rd, wr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unrecognized subcommand: %s\n", os.Args[1])
		os.Exit(1)
	}
}
