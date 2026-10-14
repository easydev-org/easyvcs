package main

import (
	"fmt"
	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
	"os"
)

func main() {
	cs, _ := store.OpenDefault()
	repo, _ := cs.OpenRepo(store.NSRef{Name: "abc-agent" + "/" + "worker"})
	id, err := object.HexToID("2c7371caeeda383fc656f86e00d9d1aa0f44bdb491939415b886a76111b05528")
	if err != nil {
		fmt.Println("hex:", err)
		return
	}
	snap, err := repo.GetSnapshot(id)
	if err != nil {
		fmt.Println("snap:", err)
		return
	}
	fmt.Println("snapshot tree:", snap.TreeID.String())
	count := 0
	var walk func(tid object.ID, prefix string)
	walk = func(tid object.ID, prefix string) {
		o, err := repo.ReadObject(tid)
		if err != nil {
			fmt.Printf("  read %s: %v\n", tid.String()[:8], err)
			return
		}
		if o.Kind != object.KindTree {
			return
		}
		for _, e := range o.Tree.SortedEntries() {
			count++
			if count <= 30 {
				fmt.Printf("  %s%s (%v)\n", prefix, e.Name, e.Kind)
			}
			if e.Kind == object.KindTree {
				walk(e.ID, prefix+e.Name+"/")
			}
		}
	}
	walk(snap.TreeID, "")
	fmt.Println("total entries:", count)
	_ = os.Stdout
}
