package tray

import _ "embed"

// IconOn is the yellow switch icon (also used for the terminal profile).
//
//go:embed icons/tray-on.ico
var IconOn []byte

// IconOff is the grey switch icon (all services stopped).
//
//go:embed icons/tray-off.ico
var IconOff []byte
