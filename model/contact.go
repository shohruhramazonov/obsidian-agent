package model

// Contact represents a person extracted from an unstructured description.
type Contact struct {
	Name         string `json:"name"`
	Birthday     string `json:"birthday"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	Telegram     string `json:"telegram"`
	LinkedIn     string `json:"linkedin"`
	Company      string `json:"company"`
	Position     string `json:"position"`
	WhereWorked  string `json:"where_worked"`
	Relationship string `json:"relationship"`
	Category     string `json:"category"`
	Projects     string `json:"projects"`
}
