package redeem

// CardTerm 是老板定下的卡档。真正生效的是 redeem_card_types 表里的行：
// 生成时把当时的天数、等级、设备和流量抄到每一张码上。改表只影响之后生成的码。
// 这里的列表用来锁住迁移种子，避免期限散落在别的文件里。
type CardTerm struct {
	Code      string
	Name      string
	Days      int
	Tier      int
	Devices   int
	Unlimited bool
}

func SeedTerms() []CardTerm {
	return []CardTerm{
		{Code: "WEEK", Name: "周卡", Days: 7, Tier: 1, Devices: 3, Unlimited: true},
		{Code: "MONTH", Name: "月卡", Days: 30, Tier: 1, Devices: 3, Unlimited: true},
		{Code: "YEAR", Name: "年卡", Days: 365, Tier: 1, Devices: 3, Unlimited: true},
	}
}
