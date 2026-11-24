package mail

import "strings"

type MailboxStatus struct {
	Messages uint32
	Unseen   uint32
	Recent   uint32
}

type Folder struct {
	Name       string
	Delimiter  string
	Attributes []string
	Status     *MailboxStatus
	Children   []*Folder
}

func (f *Folder) DisplayName() string {
	parts := strings.Split(f.Name, f.Delimiter)
	return parts[len(parts)-1]
}

func (f *Folder) IsNoSelect() bool {
	for _, a := range f.Attributes {
		if strings.EqualFold(a, `\NoSelect`) {
			return true
		}
	}
	return false
}

func (f *Folder) Depth() int {
	if f.Delimiter == "" {
		return 0
	}
	return strings.Count(f.Name, f.Delimiter)
}
