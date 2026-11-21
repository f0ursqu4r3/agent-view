package cells

// Role is a semantic color. The palette maps each role to a truecolor value.
type Role uint8

// Roles in mask order: t d f b r e c s a o x k z.
const (
	RText Role = iota
	RDim
	RFaint
	RBright
	RRead
	REdit
	RCommand
	RSearch
	RAgent
	RClaude
	RCodex
	ROK
	RFail
	RMint
)

type rgb struct{ r, g, b uint8 }

// palette follows the spec section "Color" (One Dark style).
var palette = [...]rgb{
	RText:    {0xc5, 0xc8, 0xc6},
	RDim:     {0x5c, 0x63, 0x70},
	RFaint:   {0x3a, 0x3f, 0x4b},
	RBright:  {0xff, 0xff, 0xff},
	RRead:    {0x61, 0xaf, 0xef},
	REdit:    {0xc6, 0x78, 0xdd},
	RCommand: {0x98, 0xc3, 0x79},
	RSearch:  {0xe5, 0xc0, 0x7b},
	RAgent:   {0x56, 0xb6, 0xc2},
	RClaude:  {0xe5, 0x91, 0x5a},
	RCodex:   {0x56, 0xb6, 0xc2},
	ROK:      {0x98, 0xc3, 0x79},
	RFail:    {0xe0, 0x6c, 0x75},
	RMint:    {0x71, 0xe3, 0xb7},
}

// maskLetters holds one letter per role, in Role order.
const maskLetters = "tdfbrecsaoxkzm"

// RoleOf returns the role for a call category.
func RoleOf(category string) Role {
	switch category {
	case "read":
		return RRead
	case "edit", "write":
		return REdit
	case "command":
		return RCommand
	case "search":
		return RSearch
	case "agent":
		return RAgent
	}
	return RText
}
