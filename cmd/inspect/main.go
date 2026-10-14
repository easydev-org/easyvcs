package main

import (
	"fmt"
	"github.com/easylab-platform/easyvcs/transfer"
	"os"
)

func main() {
	data, _ := os.ReadFile("/tmp/bundle.gz")
	b, err := transfer.UnmarshalBinary(data)
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Printf("objects=%d snapshots=%d revisions=%d refs=%d\n", len(b.Objects), len(b.Snapshots), len(b.Revisions), len(b.Refs))
	// 唯一内容数
	uniq := map[string]bool{}
	for _, o := range b.Objects {
		uniq[string(o.Content)] = true
	}
	fmt.Println("unique object contents:", len(uniq))
	// kind 分布
	kinds := map[string]int{}
	for _, o := range b.Objects {
		kinds[fmt.Sprint(o.Kind)]++
	}
	fmt.Println("kinds:", kinds)
	// 前几个 object
	for i, o := range b.Objects {
		if i >= 3 {
			break
		}
		fmt.Printf("  obj[%d] kind=%v len=%d\n", i, o.Kind, len(o.Content))
	}
}
