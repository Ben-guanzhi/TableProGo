package main

import _ "embed"

// The product icon, drawn by tools/iconfamily in the workspace root and embedded
// here so the window, the taskbar and the exe resource all come from one file.
//
//go:embed build/appicon.png
var appIconPNG []byte
