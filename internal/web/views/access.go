package views

// Access types are presentation DTOs. Database options contain display identity
// only; credentials must never be passed to a view.
type AccessUser struct {
	ID, Username    string
	Active          bool
	IsPlatformAdmin bool
}
type AccessGroup struct {
	ID, Name string
	Active   bool
}
type AccessDatabase struct {
	ID, Name string
	Active   bool
}
type AccessMembership struct{ UserID, GroupID, Username, GroupName string }
type AccessGrant struct{ GroupID, DatabaseID, GroupName, DatabaseName, Permission string }
type AuthorizedDatabase struct{ ID, Name, Permission string }

type AccessAdminData struct {
	CSRFToken, Username, Message, Error string
	Users                               []AccessUser
	Groups                              []AccessGroup
	Databases                           []AccessDatabase
	Memberships                         []AccessMembership
	Grants                              []AccessGrant
}
type AccessAccountData struct {
	Username, CSRFToken string
	IsPlatformAdmin     bool
	NeedsRefresh        bool
	Databases           []AuthorizedDatabase
}

type AccessPageData struct {
	CSRFToken, Username, Message, Error string
	ConnectionsAvailable                bool
	Users                               []AccessUser
	Groups                              []AccessGroup
	Databases                           []AccessDatabase
	Memberships                         []AccessMembership
	Grants                              []AccessGrant
	SelectedGroup                       AccessGroup
}

func (data AccessPageData) HasActiveDatabases() bool {
	for _, database := range data.Databases {
		if database.Active {
			return true
		}
	}
	return false
}
