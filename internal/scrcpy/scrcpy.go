package scrcpy

import _ "embed"

//go:embed scrcpy-server-v4.0.jar
var Server []byte

const Filename = "scrcpy-server-v4.0.jar"
const ServerVersion = "4.0"
const RemotePath = "/data/local/tmp/scrcpy-server.jar"
const DeviceSocket = "localabstract:scrcpy"
