package views

type AccessUser struct {
	ID       string
	Username string
	Active   bool
}

type AccessGroup struct {
	ID   string
	Name string
}

type AccessDatabase struct {
	ID   string
	Name string
}

type AccessMembership struct {
	UserID    string
	GroupID   string
	Username  string
	GroupName string
}

type AccessGrant struct {
	GroupID      string
	DatabaseID   string
	GroupName    string
	DatabaseName string
	Permission   string
}

type AuthorizedDatabase struct {
	ID         string
	Name       string
	Permission string
}

type AccessAdminData struct {
	CSRFToken   string
	Username    string
	Users       []AccessUser
	Groups      []AccessGroup
	Databases   []AccessDatabase
	Memberships []AccessMembership
	Grants      []AccessGrant
	Message     string
	Error       string
}

type AccessAccountData struct {
	Username        string
	CSRFToken       string
	IsPlatformAdmin bool
	Databases       []AuthorizedDatabase
}
