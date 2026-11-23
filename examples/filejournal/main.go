// Command filejournal writes a saga journal to a JSON-lines file and reads it back.
// Use one journal value for a directory, in one process: a second writer is caught
// only when Events reads the repeated Seq.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
