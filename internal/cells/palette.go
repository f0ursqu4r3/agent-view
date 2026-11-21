package cells

// Role is a semantic color. The palette maps each role to a truecolor value.
type Role uint8

// Background inks follow the foreground roles, starting at RPlasma.
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
	RPlasma
	REmber
	RRain
	RIndigo
	RViolet
	ROrchid
	RCoal
	RFlame
	RGold
	RDeepWater
	RFoam
	RForest
	RMatrix
	RPhosphor
)

type rgb struct{ r, g, b uint8 }

// palette follows the spec section "Color" (One Dark style).
var palette = [...]rgb{
	RText:      {0xc5, 0xc8, 0xc6},
	RDim:       {0x5c, 0x63, 0x70},
	RFaint:     {0x3a, 0x3f, 0x4b},
	RBright:    {0xff, 0xff, 0xff},
	RRead:      {0x61, 0xaf, 0xef},
	REdit:      {0xc6, 0x78, 0xdd},
	RCommand:   {0x98, 0xc3, 0x79},
	RSearch:    {0xe5, 0xc0, 0x7b},
	RAgent:     {0x56, 0xb6, 0xc2},
	RClaude:    {0xe5, 0x91, 0x5a},
	RCodex:     {0x56, 0xb6, 0xc2},
	ROK:        {0x98, 0xc3, 0x79},
	RFail:      {0xe0, 0x6c, 0x75},
	RMint:      {0x71, 0xe3, 0xb7},
	RPlasma:    {0x55, 0x40, 0x71},
	REmber:     {0x91, 0x45, 0x29},
	RRain:      {0x43, 0x69, 0x80},
	RIndigo:    {0x3d, 0x48, 0x7f},
	RViolet:    {0x75, 0x52, 0x98},
	ROrchid:    {0xab, 0x70, 0x99},
	RCoal:      {0x55, 0x2c, 0x24},
	RFlame:     {0xba, 0x63, 0x36},
	RGold:      {0xdc, 0xa1, 0x57},
	RDeepWater: {0x27, 0x3b, 0x50},
	RFoam:      {0x72, 0xa6, 0xb2},
	RForest:    {0x25, 0x49, 0x3b},
	RMatrix:    {0x42, 0x89, 0x5d},
	RPhosphor:  {0x91, 0xc9, 0x98},
}

// The mask groups background shades by palette.
const maskLetters = "tdfbrecsaoxkzmpui" + "pppuuuiiggg"

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
