package main

import (
	"fmt"
	"os"

	"github.com/easylab-platform/easyvcs/store"
	"github.com/easylab-platform/easyvcs/transfer"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: applybundle <bundle.gz>")
		os.Exit(1)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Println("bundle bytes:", len(data))
	b, err := transfer.UnmarshalBinary(data)
	if err != nil {
		fmt.Println("unmarshal:", err)
		return
	}
	fmt.Printf("bundle: snapshots=%d revisions=%d objects=%d\n", len(b.Snapshots), len(b.Revisions), len(b.Objects))
	cs, err := store.OpenDefault()
	if err != nil {
		fmt.Println("open store:", err)
		return
	}
	repo, err := cs.OpenRepo(store.NSRef{Name: "abc-agent" + "/" + "worker"})
	if err != nil {
		fmt.Println("open repo:", err)
		return
	}
	n, err := transfer.Apply(repo, b)
	if err != nil {
		fmt.Println("apply:", err)
		return
	}
	fmt.Println("applied objects:", n)
}
