package workflowclose

import (
 "context"
 "slices"
 "github.com/example-user/commander/internal/beads"
)

// A retirement marker is not proof that the requirement passed. Completion
// judges the original requirement even after an interrupted legacy retirement.
// The view changes no persisted facts or unrelated historical work.
type completionView struct{beads.Client;target string}
func(v completionView) restore(b beads.Bead)beads.Bead{if b.ID==v.target{b.Labels=slices.DeleteFunc(slices.Clone(b.Labels),func(l string)bool{return l=="retired"||l=="superseded"})};return b}
func(v completionView)Show(ctx context.Context,dir,id string)(*beads.Bead,error){b,e:=v.Client.Show(ctx,dir,id);if b!=nil{copy:=v.restore(*b);b=&copy};return b,e}
func(v completionView)List(ctx context.Context,dir string,o beads.ListOptions)([]beads.Bead,error){bs,e:=v.Client.List(ctx,dir,o);for i:=range bs{bs[i]=v.restore(bs[i])};return bs,e}
