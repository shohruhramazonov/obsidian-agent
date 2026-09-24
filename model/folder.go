package model

// Folder is a folder inside the Obsidian vault of a user's local agent that
// contacts can be saved to.
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
