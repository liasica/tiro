// Package web 内嵌前端构建产物
//
// dist 目录由 make web 生成，仓库里只保留占位文件，构建产物不入库
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Assets 返回内嵌的前端资源，前端尚未构建时返回 nil
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil
	}

	if _, err = fs.Stat(sub, "index.html"); err != nil {
		return nil
	}

	return sub
}
