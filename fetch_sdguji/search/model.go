package main

import "fmt"

// 搜索接口返回的最小字段集合，够用即可。

type Author struct {
	PersName           string `json:"persName"`
	DynastyName        string `json:"dynastyName"`
	DynastyCateID      string `json:"dynastyCateId"`
	ResponsibleTypeStr string `json:"responsibleTypeStr"`
}

type Category struct {
	CateID       string `json:"cateId"`
	CateName     string `json:"cateName"`
	ParentCateID string `json:"parentCateId"`
}

type Book struct {
	BookID              string     `json:"bookId"`
	BookName            string     `json:"bookName"`
	CheckType           int        `json:"checkType"`
	DynastyCategoryID   string     `json:"dynastyCategoryId"`
	DynastyCategoryName string     `json:"dynastyCategoryName"`
	Authors             []Author   `json:"authors"`
	TraditionalCategory []Category `json:"traditionalCategory"`
	Edition             struct {
		Edition            string `json:"edition"`
		EditionDynastyName string `json:"editionDynastyName"`
	} `json:"edition"`
	PublicationStmt struct {
		Distributor string `json:"distributor"`
	} `json:"publicationStmt"`
}

type BookHit struct {
	Book Book `json:"book"`
}

type SearchData struct {
	Total      int       `json:"total"`
	PageSize   int       `json:"pageSize"`
	PageNum    int       `json:"pageNum"`
	HasMore    bool      `json:"hasMore"`
	SearchID   string    `json:"searchId"`
	BookList   []BookHit `json:"searchBookList"`
	QueryText  string    `json:"queryText"`
	TotalType  int       `json:"totalType"`
	SearchKind string    `json:"-"`
}

type SearchResp struct {
	ErrorCode int        `json:"errorCode"`
	ErrorMsg  string     `json:"errorMsg"`
	Data      SearchData `json:"data"`
}

// checkTypeName 站点校对状态（以搜索卡片上显示的徽标为准）：
// 1=粗校，2=精校，3=AI整理，其它未知值原样标成“未知”。
func checkTypeName(t int) string {
	switch t {
	case 1:
		return "粗校"
	case 2:
		return "精校"
	case 3:
		return "AI整理"
	default:
		return fmt.Sprintf("未知(%d)", t)
	}
}
