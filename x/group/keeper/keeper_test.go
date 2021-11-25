package keeper_test

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/stretchr/testify/suite"
	tmtime "github.com/tendermint/tendermint/libs/time"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/simapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	"github.com/cosmos/cosmos-sdk/x/bank/testutil"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/group"
)

type TestSuite struct {
	suite.Suite

	app              *simapp.SimApp
	ctx              sdk.Context
	addrs            []sdk.AccAddress
	queryClient      group.QueryClient
	msgClient        group.MsgClient
	groupID          uint64
	groupAccountAddr sdk.AccAddress
	bankKeeper       bankkeeper.Keeper
	blockTime        time.Time
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(TestSuite))
}

func (s *TestSuite) SetupTest() {
	app := simapp.Setup(s.T(), false)
	ctx := app.BaseApp.NewContext(false, tmproto.Header{})
	now := tmtime.Now()
	ctx = ctx.WithBlockHeader(tmproto.Header{Time: now})
	queryHelper := baseapp.NewQueryServerTestHelper(ctx, app.InterfaceRegistry())
	group.RegisterQueryServer(queryHelper, app.GroupKeeper)
	queryClient := group.NewQueryClient(queryHelper)
	s.queryClient = queryClient

	s.app = app
	s.ctx = ctx
	s.queryClient = queryClient
	s.addrs = simapp.AddTestAddrsIncremental(app, ctx, 5, sdk.NewInt(30000000))

	// Initial group, group account and balance setup
	members := []group.Member{
		{Address: s.addrs[5].String(), Weight: "1"}, {Address: s.addrs[2].String(), Weight: "2"},
	}
	groupRes, err := s.msgClient.CreateGroup(s.ctx.Context(), &group.MsgCreateGroup{
		Admin:    s.addrs[1].String(),
		Members:  members,
		Metadata: nil,
	})
	s.Require().NoError(err)
	s.groupID = groupRes.GroupId

	policy := group.NewThresholdDecisionPolicy(
		"2",
		time.Duration(1),
		// gogotypes.Duration{Seconds: 1},
	)
	accountReq := &group.MsgCreateGroupAccount{
		Admin:    s.addrs[1].String(),
		GroupId:  s.groupID,
		Metadata: nil,
	}
	err = accountReq.SetDecisionPolicy(policy)
	s.Require().NoError(err)
	accountRes, err := s.msgClient.CreateGroupAccount(s.ctx.Context(), accountReq)
	s.Require().NoError(err)
	addr, err := sdk.AccAddressFromBech32(accountRes.Address)
	s.Require().NoError(err)
	s.groupAccountAddr = addr
	s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, s.ctx, s.groupAccountAddr, sdk.Coins{sdk.NewInt64Coin("test", 10000)}))

}

func (s *TestSuite) TestCreateGroup() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr3 := addrs[2]
	addr5 := addrs[4]
	addr6 := addrs[5]

	members := []group.Member{{
		Address:  addr5.String(),
		Weight:   "1",
		Metadata: nil,
	}, {
		Address:  addr6.String(),
		Weight:   "2",
		Metadata: nil,
	}}

	expGroups := []*group.GroupInfo{
		{
			GroupId:     s.groupID,
			Version:     1,
			Admin:       addr1.String(),
			TotalWeight: "3",
			Metadata:    nil,
		},
		{
			GroupId:     2,
			Version:     1,
			Admin:       addr1.String(),
			TotalWeight: "3",
			Metadata:    nil,
		},
	}

	specs := map[string]struct {
		req       *group.MsgCreateGroup
		expErr    bool
		expGroups []*group.GroupInfo
	}{
		"all good": {
			req: &group.MsgCreateGroup{
				Admin:    addr1.String(),
				Members:  members,
				Metadata: nil,
			},
			expGroups: expGroups,
		},
		"group metadata too long": {
			req: &group.MsgCreateGroup{
				Admin:    addr1.String(),
				Members:  members,
				Metadata: bytes.Repeat([]byte{1}, 256),
			},
			expErr: true,
		},
		"member metadata too long": {
			req: &group.MsgCreateGroup{
				Admin: addr1.String(),
				Members: []group.Member{{
					Address:  addr3.String(),
					Weight:   "1",
					Metadata: bytes.Repeat([]byte{1}, 256),
				}},
				Metadata: nil,
			},
			expErr: true,
		},
		"zero member weight": {
			req: &group.MsgCreateGroup{
				Admin: addr1.String(),
				Members: []group.Member{{
					Address:  addr3.String(),
					Weight:   "0",
					Metadata: nil,
				}},
				Metadata: nil,
			},
			expErr: true,
		},
	}

	var seq uint32 = 1
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			res, err := s.msgClient.CreateGroup(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				_, err := s.queryClient.GroupInfo(ctx.Context(), &group.QueryGroupInfo{GroupId: uint64(seq + 1)})
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			id := res.GroupId

			seq++
			s.Assert().Equal(uint64(seq), id)

			// then all data persisted
			loadedGroupRes, err := s.queryClient.GroupInfo(ctx.Context(), &group.QueryGroupInfo{GroupId: id})
			s.Require().NoError(err)
			s.Assert().Equal(spec.req.Admin, loadedGroupRes.Info.Admin)
			s.Assert().Equal(spec.req.Metadata, loadedGroupRes.Info.Metadata)
			s.Assert().Equal(id, loadedGroupRes.Info.GroupId)
			s.Assert().Equal(uint64(1), loadedGroupRes.Info.Version)

			// and members are stored as well
			membersRes, err := s.queryClient.GroupMembers(ctx.Context(), &group.QueryGroupMembers{GroupId: id})
			s.Require().NoError(err)
			loadedMembers := membersRes.Members
			s.Require().Equal(len(members), len(loadedMembers))
			// we reorder members by address to be able to compare them
			sort.Slice(members, func(i, j int) bool {
				addri, err := sdk.AccAddressFromBech32(members[i].Address)
				s.Require().NoError(err)
				addrj, err := sdk.AccAddressFromBech32(members[j].Address)
				s.Require().NoError(err)
				return bytes.Compare(addri, addrj) < 0
			})
			for i := range loadedMembers {
				s.Assert().Equal(members[i].Metadata, loadedMembers[i].Member.Metadata)
				s.Assert().Equal(members[i].Address, loadedMembers[i].Member.Address)
				s.Assert().Equal(members[i].Weight, loadedMembers[i].Member.Weight)
				s.Assert().Equal(id, loadedMembers[i].GroupId)
			}

			// query groups by admin
			groupsRes, err := s.queryClient.GroupsByAdmin(ctx.Context(), &group.QueryGroupsByAdmin{Admin: addr1.String()})
			s.Require().NoError(err)
			loadedGroups := groupsRes.Groups
			s.Require().Equal(len(spec.expGroups), len(loadedGroups))
			for i := range loadedGroups {
				s.Assert().Equal(spec.expGroups[i].Metadata, loadedGroups[i].Metadata)
				s.Assert().Equal(spec.expGroups[i].Admin, loadedGroups[i].Admin)
				s.Assert().Equal(spec.expGroups[i].TotalWeight, loadedGroups[i].TotalWeight)
				s.Assert().Equal(spec.expGroups[i].GroupId, loadedGroups[i].GroupId)
				s.Assert().Equal(spec.expGroups[i].Version, loadedGroups[i].Version)
			}
		})
	}

}

func (s *TestSuite) TestUpdateGroupAdmin() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr3 := addrs[2]
	addr4 := addrs[3]

	members := []group.Member{{
		Address:  addr1.String(),
		Weight:   "1",
		Metadata: nil,
	}}
	oldAdmin := addr2.String()
	newAdmin := addr3.String()
	groupRes, err := s.msgClient.CreateGroup(ctx.Context(), &group.MsgCreateGroup{
		Admin:    oldAdmin,
		Members:  members,
		Metadata: nil,
	})
	s.Require().NoError(err)
	groupID := groupRes.GroupId
	specs := map[string]struct {
		req       *group.MsgUpdateGroupAdmin
		expStored *group.GroupInfo
		expErr    bool
	}{
		"with correct admin": {
			req: &group.MsgUpdateGroupAdmin{
				GroupId:  groupID,
				Admin:    oldAdmin,
				NewAdmin: newAdmin,
			},
			expStored: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       newAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     2,
			},
		},
		"with wrong admin": {
			req: &group.MsgUpdateGroupAdmin{
				GroupId:  groupID,
				Admin:    addr4.String(),
				NewAdmin: newAdmin,
			},
			expErr: true,
			expStored: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       oldAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
		},
		"with unknown groupID": {
			req: &group.MsgUpdateGroupAdmin{
				GroupId:  999,
				Admin:    oldAdmin,
				NewAdmin: newAdmin,
			},
			expErr: true,
			expStored: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       oldAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			_, err := s.msgClient.UpdateGroupAdmin(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// then
			res, err := s.queryClient.GroupInfo(ctx.Context(), &group.QueryGroupInfo{GroupId: groupID})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expStored, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupMetadata() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr3 := addrs[2]

	oldAdmin := addr1.String()
	groupID := s.groupID

	specs := map[string]struct {
		req       *group.MsgUpdateGroupMetadata
		expErr    bool
		expStored *group.GroupInfo
	}{
		"with correct admin": {
			req: &group.MsgUpdateGroupMetadata{
				GroupId:  groupID,
				Admin:    oldAdmin,
				Metadata: []byte{1, 2, 3},
			},
			expStored: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       oldAdmin,
				Metadata:    []byte{1, 2, 3},
				TotalWeight: "3",
				Version:     2,
			},
		},
		"with wrong admin": {
			req: &group.MsgUpdateGroupMetadata{
				GroupId:  groupID,
				Admin:    addr3.String(),
				Metadata: []byte{1, 2, 3},
			},
			expErr: true,
			expStored: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       oldAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
		},
		"with unknown groupid": {
			req: &group.MsgUpdateGroupMetadata{
				GroupId:  999,
				Admin:    oldAdmin,
				Metadata: []byte{1, 2, 3},
			},
			expErr: true,
			expStored: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       oldAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			_, err := s.msgClient.UpdateGroupMetadata(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// then
			res, err := s.queryClient.GroupInfo(ctx.Context(), &group.QueryGroupInfo{GroupId: groupID})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expStored, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupMembers() {
	ctx, addrs := s.ctx, s.addrs
	addr3 := addrs[2]
	addr4 := addrs[3]
	addr5 := addrs[4]
	addr6 := addrs[5]

	member1 := addr5.String()
	member2 := addr6.String()
	members := []group.Member{{
		Address:  member1,
		Weight:   "1",
		Metadata: nil,
	}}

	myAdmin := addr4.String()
	groupRes, err := s.msgClient.CreateGroup(ctx.Context(), &group.MsgCreateGroup{
		Admin:    myAdmin,
		Members:  members,
		Metadata: nil,
	})
	s.Require().NoError(err)
	groupID := groupRes.GroupId

	specs := map[string]struct {
		req        *group.MsgUpdateGroupMembers
		expErr     bool
		expGroup   *group.GroupInfo
		expMembers []*group.GroupMember
	}{
		"add new member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address:  member2,
					Weight:   "2",
					Metadata: nil,
				}},
			},
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "3",
				Version:     2,
			},
			expMembers: []*group.GroupMember{
				{
					Member: &group.Member{
						Address:  member2,
						Weight:   "2",
						Metadata: nil,
					},
					GroupId: groupID,
				},
				{
					Member: &group.Member{
						Address:  member1,
						Weight:   "1",
						Metadata: nil,
					},
					GroupId: groupID,
				},
			},
		},
		"update member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address:  member1,
					Weight:   "2",
					Metadata: []byte{1, 2, 3},
				}},
			},
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "2",
				Version:     2,
			},
			expMembers: []*group.GroupMember{
				{
					GroupId: groupID,
					Member: &group.Member{
						Address:  member1,
						Weight:   "2",
						Metadata: []byte{1, 2, 3},
					},
				},
			},
		},
		"update member with same data": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: member1,
					Weight:  "1",
				}},
			},
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     2,
			},
			expMembers: []*group.GroupMember{
				{
					GroupId: groupID,
					Member: &group.Member{
						Address: member1,
						Weight:  "1",
					},
				},
			},
		},
		"replace member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{
					{
						Address:  member1,
						Weight:   "0",
						Metadata: nil,
					},
					{
						Address:  member2,
						Weight:   "1",
						Metadata: nil,
					},
				},
			},
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     2,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address:  member2,
					Weight:   "1",
					Metadata: nil,
				},
			}},
		},
		"remove existing member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address:  member1,
					Weight:   "0",
					Metadata: nil,
				}},
			},
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "0",
				Version:     2,
			},
			expMembers: []*group.GroupMember{},
		},
		"remove unknown member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address:  addr4.String(),
					Weight:   "0",
					Metadata: nil,
				}},
			},
			expErr: true,
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address:  member1,
					Weight:   "1",
					Metadata: nil,
				},
			}},
		},
		"with wrong admin": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   addr3.String(),
				MemberUpdates: []group.Member{{
					Address:  member1,
					Weight:   "2",
					Metadata: nil,
				}},
			},
			expErr: true,
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address: member1,
					Weight:  "1",
				},
			}},
		},
		"with unknown groupID": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: 999,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address:  member1,
					Weight:   "2",
					Metadata: nil,
				}},
			},
			expErr: true,
			expGroup: &group.GroupInfo{
				GroupId:     groupID,
				Admin:       myAdmin,
				Metadata:    nil,
				TotalWeight: "1",
				Version:     1,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address: member1,
					Weight:  "1",
				},
			}},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			_, err := s.msgClient.UpdateGroupMembers(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// then
			res, err := s.queryClient.GroupInfo(ctx.Context(), &group.QueryGroupInfo{GroupId: groupID})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroup, res.Info)

			// and members persisted
			membersRes, err := s.queryClient.GroupMembers(ctx.Context(), &group.QueryGroupMembers{GroupId: groupID})
			s.Require().NoError(err)
			loadedMembers := membersRes.Members
			s.Require().Equal(len(spec.expMembers), len(loadedMembers))
			// we reorder group members by address to be able to compare them
			sort.Slice(spec.expMembers, func(i, j int) bool {
				addri, err := sdk.AccAddressFromBech32(spec.expMembers[i].Member.Address)
				s.Require().NoError(err)
				addrj, err := sdk.AccAddressFromBech32(spec.expMembers[j].Member.Address)
				s.Require().NoError(err)
				return bytes.Compare(addri, addrj) < 0
			})
			for i := range loadedMembers {
				s.Assert().Equal(spec.expMembers[i].Member.Metadata, loadedMembers[i].Member.Metadata)
				s.Assert().Equal(spec.expMembers[i].Member.Address, loadedMembers[i].Member.Address)
				s.Assert().Equal(spec.expMembers[i].Member.Weight, loadedMembers[i].Member.Weight)
				s.Assert().Equal(spec.expMembers[i].GroupId, loadedMembers[i].GroupId)
			}
		})
	}
}

func (s *TestSuite) TestCreateGroupAccount() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr4 := addrs[3]

	groupRes, err := s.msgClient.CreateGroup(ctx.Context(), &group.MsgCreateGroup{
		Admin:    addr1.String(),
		Members:  nil,
		Metadata: nil,
	})
	s.Require().NoError(err)
	myGroupID := groupRes.GroupId

	specs := map[string]struct {
		req    *group.MsgCreateGroupAccount
		policy group.DecisionPolicy
		expErr bool
	}{
		"all good": {
			req: &group.MsgCreateGroupAccount{
				Admin:    addr1.String(),
				Metadata: nil,
				GroupId:  myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Duration(1),
			),
		},
		"decision policy threshold > total group weight": {
			req: &group.MsgCreateGroupAccount{
				Admin:    addr1.String(),
				Metadata: nil,
				GroupId:  myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"10",
				time.Duration(1),
			),
		},
		"group id does not exists": {
			req: &group.MsgCreateGroupAccount{
				Admin:    addr1.String(),
				Metadata: nil,
				GroupId:  9999,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Duration(1),
			),
			expErr: true,
		},
		"admin not group admin": {
			req: &group.MsgCreateGroupAccount{
				Admin:    addr4.String(),
				Metadata: nil,
				GroupId:  myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Duration(1),
			),
			expErr: true,
		},
		"metadata too long": {
			req: &group.MsgCreateGroupAccount{
				Admin:    addr1.String(),
				Metadata: []byte(strings.Repeat("a", 256)),
				GroupId:  myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Duration(1),
			),
			expErr: true,
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			err := spec.req.SetDecisionPolicy(spec.policy)
			s.Require().NoError(err)

			res, err := s.msgClient.CreateGroupAccount(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			addr := res.Address

			// then all data persisted
			groupAccountRes, err := s.queryClient.GroupAccountInfo(ctx.Context(), &group.QueryGroupAccountInfo{Address: addr})
			s.Require().NoError(err)

			groupAccount := groupAccountRes.Info
			s.Assert().Equal(addr, groupAccount.Address)
			s.Assert().Equal(myGroupID, groupAccount.GroupId)
			s.Assert().Equal(spec.req.Admin, groupAccount.Admin)
			s.Assert().Equal(spec.req.Metadata, groupAccount.Metadata)
			s.Assert().Equal(uint64(1), groupAccount.Version)
			s.Assert().Equal(spec.policy.(*group.ThresholdDecisionPolicy), groupAccount.GetDecisionPolicy())
		})
	}
}

func (s *TestSuite) TestUpdateGroupAccountAdmin() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr5 := addrs[4]

	admin, newAdmin := addr1, addr2
	groupAccountAddr, myGroupID, policy, derivationKey := createGroupAndGroupAccount(admin, s)

	specs := map[string]struct {
		req             *group.MsgUpdateGroupAccountAdmin
		expGroupAccount *group.GroupAccountInfo
		expErr          bool
	}{
		"with wrong admin": {
			req: &group.MsgUpdateGroupAccountAdmin{
				Admin:    addr5.String(),
				Address:  groupAccountAddr,
				NewAdmin: newAdmin.String(),
			},
			expGroupAccount: &group.GroupAccountInfo{
				Admin:          admin.String(),
				Address:        groupAccountAddr,
				GroupId:        myGroupID,
				Metadata:       nil,
				Version:        2,
				DecisionPolicy: nil,
				DerivationKey:  derivationKey,
			},
			expErr: true,
		},
		"with wrong group account": {
			req: &group.MsgUpdateGroupAccountAdmin{
				Admin:    admin.String(),
				Address:  addr5.String(),
				NewAdmin: newAdmin.String(),
			},
			expGroupAccount: &group.GroupAccountInfo{
				Admin:          admin.String(),
				Address:        groupAccountAddr,
				GroupId:        myGroupID,
				Metadata:       nil,
				Version:        2,
				DecisionPolicy: nil,
				DerivationKey:  derivationKey,
			},
			expErr: true,
		},
		"correct data": {
			req: &group.MsgUpdateGroupAccountAdmin{
				Admin:    admin.String(),
				Address:  groupAccountAddr,
				NewAdmin: newAdmin.String(),
			},
			expGroupAccount: &group.GroupAccountInfo{
				Admin:          newAdmin.String(),
				Address:        groupAccountAddr,
				GroupId:        myGroupID,
				Metadata:       nil,
				Version:        2,
				DecisionPolicy: nil,
				DerivationKey:  derivationKey,
			},
			expErr: false,
		},
	}
	for msg, spec := range specs {
		spec := spec
		err := spec.expGroupAccount.SetDecisionPolicy(policy)
		s.Require().NoError(err)

		s.Run(msg, func() {
			_, err := s.msgClient.UpdateGroupAccountAdmin(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			res, err := s.queryClient.GroupAccountInfo(ctx.Context(), &group.QueryGroupAccountInfo{
				Address: groupAccountAddr,
			})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroupAccount, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupAccountMetadata() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr5 := addrs[4]

	admin := addr1
	groupAccountAddr, myGroupID, policy, derivationKey := createGroupAndGroupAccount(admin, s)

	specs := map[string]struct {
		req             *group.MsgUpdateGroupAccountMetadata
		expGroupAccount *group.GroupAccountInfo
		expErr          bool
	}{
		"with wrong admin": {
			req: &group.MsgUpdateGroupAccountMetadata{
				Admin:    addr5.String(),
				Address:  groupAccountAddr,
				Metadata: []byte("hello"),
			},
			expGroupAccount: &group.GroupAccountInfo{},
			expErr:          true,
		},
		"with wrong group account": {
			req: &group.MsgUpdateGroupAccountMetadata{
				Admin:    admin.String(),
				Address:  addr5.String(),
				Metadata: []byte("hello"),
			},
			expGroupAccount: &group.GroupAccountInfo{},
			expErr:          true,
		},
		"with comment too long": {
			req: &group.MsgUpdateGroupAccountMetadata{
				Admin:    admin.String(),
				Address:  addr5.String(),
				Metadata: []byte(strings.Repeat("a", 256)),
			},
			expGroupAccount: &group.GroupAccountInfo{},
			expErr:          true,
		},
		"correct data": {
			req: &group.MsgUpdateGroupAccountMetadata{
				Admin:    admin.String(),
				Address:  groupAccountAddr,
				Metadata: []byte("hello"),
			},
			expGroupAccount: &group.GroupAccountInfo{
				Admin:          admin.String(),
				Address:        groupAccountAddr,
				GroupId:        myGroupID,
				Metadata:       []byte("hello"),
				Version:        2,
				DecisionPolicy: nil,
				DerivationKey:  derivationKey,
			},
			expErr: false,
		},
	}
	for msg, spec := range specs {
		spec := spec
		err := spec.expGroupAccount.SetDecisionPolicy(policy)
		s.Require().NoError(err)

		s.Run(msg, func() {
			_, err := s.msgClient.UpdateGroupAccountMetadata(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			res, err := s.queryClient.GroupAccountInfo(ctx.Context(), &group.QueryGroupAccountInfo{
				Address: groupAccountAddr,
			})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroupAccount, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupAccountDecisionPolicy() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr5 := addrs[4]

	admin := addr1
	groupAccountAddr, myGroupID, policy, derivationKey := createGroupAndGroupAccount(admin, s)

	specs := map[string]struct {
		req             *group.MsgUpdateGroupAccountDecisionPolicy
		policy          group.DecisionPolicy
		expGroupAccount *group.GroupAccountInfo
		expErr          bool
	}{
		"with wrong admin": {
			req: &group.MsgUpdateGroupAccountDecisionPolicy{
				Admin:   addr5.String(),
				Address: groupAccountAddr,
			},
			policy:          policy,
			expGroupAccount: &group.GroupAccountInfo{},
			expErr:          true,
		},
		"with wrong group account": {
			req: &group.MsgUpdateGroupAccountDecisionPolicy{
				Admin:   admin.String(),
				Address: addr5.String(),
			},
			policy:          policy,
			expGroupAccount: &group.GroupAccountInfo{},
			expErr:          true,
		},
		"correct data": {
			req: &group.MsgUpdateGroupAccountDecisionPolicy{
				Admin:   admin.String(),
				Address: groupAccountAddr,
			},
			policy: group.NewThresholdDecisionPolicy(
				"2",
				time.Duration(2),
			),
			expGroupAccount: &group.GroupAccountInfo{
				Admin:          admin.String(),
				Address:        groupAccountAddr,
				GroupId:        myGroupID,
				Metadata:       nil,
				Version:        2,
				DecisionPolicy: nil,
				DerivationKey:  derivationKey,
			},
			expErr: false,
		},
	}
	for msg, spec := range specs {
		spec := spec
		err := spec.expGroupAccount.SetDecisionPolicy(spec.policy)
		s.Require().NoError(err)

		err = spec.req.SetDecisionPolicy(spec.policy)
		s.Require().NoError(err)

		s.Run(msg, func() {
			_, err := s.msgClient.UpdateGroupAccountDecisionPolicy(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			res, err := s.queryClient.GroupAccountInfo(ctx.Context(), &group.QueryGroupAccountInfo{
				Address: groupAccountAddr,
			})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroupAccount, res.Info)
		})
	}
}

func (s *TestSuite) TestGroupAccountsByAdminOrGroup() {
	ctx, addrs := s.ctx, s.addrs
	addr2 := addrs[1]

	admin := addr2
	groupRes, err := s.msgClient.CreateGroup(ctx.Context(), &group.MsgCreateGroup{
		Admin:    admin.String(),
		Members:  nil,
		Metadata: nil,
	})
	s.Require().NoError(err)
	myGroupID := groupRes.GroupId

	policies := []group.DecisionPolicy{
		group.NewThresholdDecisionPolicy(
			"1",
			time.Duration(1),
		),
		group.NewThresholdDecisionPolicy(
			"10",
			time.Duration(1),
		),
	}

	count := 2
	expectAccs := make([]*group.GroupAccountInfo, count)
	for i := range expectAccs {
		req := &group.MsgCreateGroupAccount{
			Admin:    admin.String(),
			Metadata: nil,
			GroupId:  myGroupID,
		}
		err := req.SetDecisionPolicy(policies[i])
		s.Require().NoError(err)
		res, err := s.msgClient.CreateGroupAccount(ctx.Context(), req)
		s.Require().NoError(err)

		expectAcc := &group.GroupAccountInfo{
			Address:  res.Address,
			Admin:    admin.String(),
			Metadata: nil,
			GroupId:  myGroupID,
			Version:  uint64(1),
		}
		err = expectAcc.SetDecisionPolicy(policies[i])
		s.Require().NoError(err)
		expectAccs[i] = expectAcc
	}
	sort.Slice(expectAccs, func(i, j int) bool { return expectAccs[i].Address < expectAccs[j].Address })

	// query group account by group
	accountsByGroupRes, err := s.queryClient.GroupAccountsByGroup(ctx.Context(), &group.QueryGroupAccountsByGroup{
		GroupId: myGroupID,
	})
	s.Require().NoError(err)
	accounts := accountsByGroupRes.GroupAccounts
	s.Require().Equal(len(accounts), count)
	// we reorder accounts by address to be able to compare them
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Address < accounts[j].Address })
	for i := range accounts {
		s.Assert().Equal(accounts[i].Address, expectAccs[i].Address)
		s.Assert().Equal(accounts[i].GroupId, expectAccs[i].GroupId)
		s.Assert().Equal(accounts[i].Admin, expectAccs[i].Admin)
		s.Assert().Equal(accounts[i].Metadata, expectAccs[i].Metadata)
		s.Assert().Equal(accounts[i].Version, expectAccs[i].Version)
		s.Assert().Equal(accounts[i].GetDecisionPolicy(), expectAccs[i].GetDecisionPolicy())
	}

	// query group account by admin
	accountsByAdminRes, err := s.queryClient.GroupAccountsByAdmin(ctx.Context(), &group.QueryGroupAccountsByAdmin{
		Admin: admin.String(),
	})
	s.Require().NoError(err)
	accounts = accountsByAdminRes.GroupAccounts
	s.Require().Equal(len(accounts), count)
	// we reorder accounts by address to be able to compare them
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Address < accounts[j].Address })
	for i := range accounts {
		s.Assert().Equal(accounts[i].Address, expectAccs[i].Address)
		s.Assert().Equal(accounts[i].GroupId, expectAccs[i].GroupId)
		s.Assert().Equal(accounts[i].Admin, expectAccs[i].Admin)
		s.Assert().Equal(accounts[i].Metadata, expectAccs[i].Metadata)
		s.Assert().Equal(accounts[i].Version, expectAccs[i].Version)
		s.Assert().Equal(accounts[i].GetDecisionPolicy(), expectAccs[i].GetDecisionPolicy())
	}
}

func (s *TestSuite) TestCreateProposal() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr4 := addrs[3]
	addr5 := addrs[4]

	myGroupID := s.groupID
	accountAddr := s.groupAccountAddr

	msgSend := &banktypes.MsgSend{
		FromAddress: s.groupAccountAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}

	accountReq := &group.MsgCreateGroupAccount{
		Admin:    addr1.String(),
		GroupId:  myGroupID,
		Metadata: nil,
	}
	policy := group.NewThresholdDecisionPolicy(
		"100",
		time.Duration(1),
	)
	err := accountReq.SetDecisionPolicy(policy)
	s.Require().NoError(err)
	bigThresholdRes, err := s.msgClient.CreateGroupAccount(ctx.Context(), accountReq)
	s.Require().NoError(err)
	bigThresholdAddr := bigThresholdRes.Address

	defaultProposal := group.Proposal{
		Status: group.ProposalStatusSubmitted,
		Result: group.ProposalResultUnfinalized,
		VoteState: group.Tally{
			YesCount:     "0",
			NoCount:      "0",
			AbstainCount: "0",
			VetoCount:    "0",
		},
		ExecutorResult: group.ProposalExecutorResultNotRun,
	}
	specs := map[string]struct {
		req         *group.MsgCreateProposal
		msgs        []sdk.Msg
		expProposal group.Proposal
		expErr      bool
		postRun     func(sdkCtx sdk.Context)
	}{
		"all good with minimal fields set": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr2.String()},
			},
			expProposal: defaultProposal,
			postRun:     func(sdkCtx sdk.Context) {},
		},
		"all good with good msg payload": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr2.String()},
			},
			msgs: []sdk.Msg{&banktypes.MsgSend{
				FromAddress: accountAddr.String(),
				ToAddress:   addr2.String(),
				Amount:      sdk.Coins{sdk.NewInt64Coin("token", 100)},
			}},
			expProposal: defaultProposal,
			postRun:     func(sdkCtx sdk.Context) {},
		},
		"metadata too long": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Metadata:  bytes.Repeat([]byte{1}, 256),
				Proposers: []string{addr2.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"group account required": {
			req: &group.MsgCreateProposal{
				Metadata:  nil,
				Proposers: []string{addr2.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"existing group account required": {
			req: &group.MsgCreateProposal{
				Address:   addr1.String(),
				Proposers: []string{addr2.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"impossible case: decision policy threshold > total group weight": {
			req: &group.MsgCreateProposal{
				Address:   bigThresholdAddr,
				Proposers: []string{addr2.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"only group members can create a proposal": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr4.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"all proposers must be in group": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr2.String(), addr4.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"proposers must not be empty": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr2.String(), ""},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"admin that is not a group member can not create proposal": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Metadata:  nil,
				Proposers: []string{addr1.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"reject msgs that are not authz by group account": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Metadata:  nil,
				Proposers: []string{addr2.String()},
			},
			msgs:    []sdk.Msg{&group.MsgAuthenticated{Signers: []sdk.AccAddress{addr1}}},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"with try exec": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr2.String()},
				Exec:      group.Exec_EXEC_TRY,
			},
			msgs: []sdk.Msg{msgSend},
			expProposal: group.Proposal{
				Status: group.ProposalStatusClosed,
				Result: group.ProposalResultAccepted,
				VoteState: group.Tally{
					YesCount:     "2",
					NoCount:      "0",
					AbstainCount: "0",
					VetoCount:    "0",
				},
				ExecutorResult: group.ProposalExecutorResultSuccess,
			},
			postRun: func(sdkCtx sdk.Context) {
				fromBalances := s.bankKeeper.GetAllBalances(sdkCtx, accountAddr)
				s.Require().Equal(sdk.Coins{sdk.NewInt64Coin("test", 9900)}, fromBalances)
				toBalances := s.bankKeeper.GetAllBalances(sdkCtx, addr2)
				s.Require().Equal(sdk.Coins{sdk.NewInt64Coin("test", 100)}, toBalances)
			},
		},
		"with try exec, not enough yes votes for proposal to pass": {
			req: &group.MsgCreateProposal{
				Address:   accountAddr.String(),
				Proposers: []string{addr5.String()},
				Exec:      group.Exec_EXEC_TRY,
			},
			msgs: []sdk.Msg{msgSend},
			expProposal: group.Proposal{
				Status: group.ProposalStatusSubmitted,
				Result: group.ProposalResultUnfinalized,
				VoteState: group.Tally{
					YesCount:     "1",
					NoCount:      "0",
					AbstainCount: "0",
					VetoCount:    "0",
				},
				ExecutorResult: group.ProposalExecutorResultNotRun,
			},
			postRun: func(sdkCtx sdk.Context) {},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			err := spec.req.SetMsgs(spec.msgs)
			s.Require().NoError(err)

			res, err := s.msgClient.CreateProposal(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			id := res.ProposalId

			// then all data persisted
			proposalRes, err := s.queryClient.Proposal(ctx.Context(), &group.QueryProposal{ProposalId: id})
			s.Require().NoError(err)
			proposal := proposalRes.Proposal

			s.Assert().Equal(accountAddr.String(), proposal.Address)
			s.Assert().Equal(spec.req.Metadata, proposal.Metadata)
			s.Assert().Equal(spec.req.Proposers, proposal.Proposers)

			psubmittedAt, err := gogotypes.TimestampProto(proposal.SubmittedAt)
			s.Require().NoError(err)
			submittedAt, err := gogotypes.TimestampFromProto(psubmittedAt)
			s.Require().NoError(err)
			s.Assert().Equal(s.blockTime, submittedAt)

			s.Assert().Equal(uint64(1), proposal.GroupVersion)
			s.Assert().Equal(uint64(1), proposal.GroupAccountVersion)
			s.Assert().Equal(spec.expProposal.Status, proposal.Status)
			s.Assert().Equal(spec.expProposal.Result, proposal.Result)
			s.Assert().Equal(spec.expProposal.VoteState, proposal.VoteState)
			s.Assert().Equal(spec.expProposal.ExecutorResult, proposal.ExecutorResult)

			ptimeout, err := gogotypes.TimestampProto(proposal.Timeout)
			s.Require().NoError(err)
			timeout, err := gogotypes.TimestampFromProto(ptimeout)
			s.Require().NoError(err)
			s.Assert().Equal(s.blockTime.Add(time.Second), timeout)

			if spec.msgs == nil { // then empty list is ok
				s.Assert().Len(proposal.GetMsgs(), 0)
			} else {
				s.Assert().Equal(spec.msgs, proposal.GetMsgs())
			}

			spec.postRun(s.ctx)
		})
	}
}

func (s *TestSuite) TestVote() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr3 := addrs[2]
	addr4 := addrs[3]
	addr5 := addrs[4]
	members := []group.Member{
		{Address: addr4.String(), Weight: "1"},
		{Address: addr3.String(), Weight: "2"},
	}
	groupRes, err := s.msgClient.CreateGroup(ctx.Context(), &group.MsgCreateGroup{
		Admin:    addr1.String(),
		Members:  members,
		Metadata: nil,
	})
	s.Require().NoError(err)
	myGroupID := groupRes.GroupId

	policy := group.NewThresholdDecisionPolicy(
		"2",
		time.Duration(2),
	)
	accountReq := &group.MsgCreateGroupAccount{
		Admin:    addr1.String(),
		GroupId:  myGroupID,
		Metadata: nil,
	}
	err = accountReq.SetDecisionPolicy(policy)
	s.Require().NoError(err)
	accountRes, err := s.msgClient.CreateGroupAccount(ctx.Context(), accountReq)
	s.Require().NoError(err)
	accountAddr := accountRes.Address
	groupAccount, err := sdk.AccAddressFromBech32(accountAddr)
	s.Require().NoError(err)
	s.Require().NotNil(groupAccount)

	s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, s.ctx, s.groupAccountAddr, sdk.Coins{sdk.NewInt64Coin("test", 10000)}))

	req := &group.MsgCreateProposal{
		Address:   accountAddr,
		Metadata:  nil,
		Proposers: []string{addr4.String()},
		Msgs:      nil,
	}
	err = req.SetMsgs([]sdk.Msg{&banktypes.MsgSend{
		FromAddress: accountAddr,
		ToAddress:   addr5.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}})
	s.Require().NoError(err)

	proposalRes, err := s.msgClient.CreateProposal(ctx.Context(), req)
	s.Require().NoError(err)
	myProposalID := proposalRes.ProposalId

	// proposals by group account
	proposalsRes, err := s.queryClient.ProposalsByGroupAccount(ctx.Context(), &group.QueryProposalsByGroupAccount{
		Address: accountAddr,
	})
	s.Require().NoError(err)
	proposals := proposalsRes.Proposals
	s.Require().Equal(len(proposals), 1)
	s.Assert().Equal(req.Address, proposals[0].Address)
	s.Assert().Equal(req.Metadata, proposals[0].Metadata)
	s.Assert().Equal(req.Proposers, proposals[0].Proposers)

	psubmittedAt, err := gogotypes.TimestampProto(proposals[0].SubmittedAt)
	s.Require().NoError(err)
	submittedAt, err := gogotypes.TimestampFromProto(psubmittedAt)
	s.Require().NoError(err)
	s.Assert().Equal(s.blockTime, submittedAt)

	s.Assert().Equal(uint64(1), proposals[0].GroupVersion)
	s.Assert().Equal(uint64(1), proposals[0].GroupAccountVersion)
	s.Assert().Equal(group.ProposalStatusSubmitted, proposals[0].Status)
	s.Assert().Equal(group.ProposalResultUnfinalized, proposals[0].Result)
	s.Assert().Equal(group.Tally{
		YesCount:     "0",
		NoCount:      "0",
		AbstainCount: "0",
		VetoCount:    "0",
	}, proposals[0].VoteState)

	specs := map[string]struct {
		srcCtx            sdk.Context
		expVoteState      group.Tally
		req               *group.MsgVote
		doBefore          func(ctx context.Context)
		postRun           func(sdkCtx sdk.Context)
		expProposalStatus group.Proposal_Status
		expResult         group.Proposal_Result
		expExecutorResult group.Proposal_ExecutorResult
		expErr            bool
	}{
		"vote yes": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_YES,
			},
			expVoteState: group.Tally{
				YesCount:     "1",
				NoCount:      "0",
				AbstainCount: "0",
				VetoCount:    "0",
			},
			expProposalStatus: group.ProposalStatusSubmitted,
			expResult:         group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"with try exec": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr3.String(),
				Choice:     group.Choice_CHOICE_YES,
				Exec:       group.Exec_EXEC_TRY,
			},
			expVoteState: group.Tally{
				YesCount:     "2",
				NoCount:      "0",
				AbstainCount: "0",
				VetoCount:    "0",
			},
			expProposalStatus: group.ProposalStatusClosed,
			expResult:         group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultSuccess,
			postRun: func(sdkCtx sdk.Context) {
				fromBalances := s.bankKeeper.GetAllBalances(sdkCtx, groupAccount)
				s.Require().Equal(sdk.Coins{sdk.NewInt64Coin("test", 9900)}, fromBalances)
				toBalances := s.bankKeeper.GetAllBalances(sdkCtx, addr2)
				s.Require().Equal(sdk.Coins{sdk.NewInt64Coin("test", 100)}, toBalances)
			},
		},
		"with try exec, not enough yes votes for proposal to pass": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_YES,
				Exec:       group.Exec_EXEC_TRY,
			},
			expVoteState: group.Tally{
				YesCount:     "1",
				NoCount:      "0",
				AbstainCount: "0",
				VetoCount:    "0",
			},
			expProposalStatus: group.ProposalStatusSubmitted,
			expResult:         group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"vote no": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			expVoteState: group.Tally{
				YesCount:     "0",
				NoCount:      "1",
				AbstainCount: "0",
				VetoCount:    "0",
			},
			expProposalStatus: group.ProposalStatusSubmitted,
			expResult:         group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"vote abstain": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_ABSTAIN,
			},
			expVoteState: group.Tally{
				YesCount:     "0",
				NoCount:      "0",
				AbstainCount: "1",
				VetoCount:    "0",
			},
			expProposalStatus: group.ProposalStatusSubmitted,
			expResult:         group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"vote veto": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_VETO,
			},
			expVoteState: group.Tally{
				YesCount:     "0",
				NoCount:      "0",
				AbstainCount: "0",
				VetoCount:    "1",
			},
			expProposalStatus: group.ProposalStatusSubmitted,
			expResult:         group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"apply decision policy early": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr3.String(),
				Choice:     group.Choice_CHOICE_YES,
			},
			expVoteState: group.Tally{
				YesCount:     "2",
				NoCount:      "0",
				AbstainCount: "0",
				VetoCount:    "0",
			},
			expProposalStatus: group.ProposalStatusClosed,
			expResult:         group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultNotRun,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"reject new votes when final decision is made already": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_YES,
			},
			doBefore: func(ctx context.Context) {
				_, err := s.msgClient.Vote(ctx, &group.MsgVote{
					ProposalId: myProposalID,
					Voter:      addr3.String(),
					Choice:     group.Choice_CHOICE_VETO,
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"metadata too long": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Metadata:   bytes.Repeat([]byte{1}, 256),
				Choice:     group.Choice_CHOICE_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"existing proposal required": {
			req: &group.MsgVote{
				ProposalId: 999,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"empty choice": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"invalid choice": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     5,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"voter must be in group": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr2.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"voter must not be empty": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      "",
				Choice:     group.Choice_CHOICE_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"voters must not be nil": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Choice:     group.Choice_CHOICE_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"admin that is not a group member can not vote": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr1.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"on timeout": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			srcCtx:  s.ctx.WithBlockTime(s.blockTime.Add(time.Second)),
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"closed already": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			doBefore: func(ctx context.Context) {
				_, err := s.msgClient.Vote(ctx, &group.MsgVote{
					ProposalId: myProposalID,
					Voter:      addr3.String(),
					Choice:     group.Choice_CHOICE_YES,
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"voted already": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			doBefore: func(ctx context.Context) {
				_, err := s.msgClient.Vote(ctx, &group.MsgVote{
					ProposalId: myProposalID,
					Voter:      addr4.String(),
					Choice:     group.Choice_CHOICE_YES,
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"with group modified": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			doBefore: func(ctx context.Context) {
				_, err = s.msgClient.UpdateGroupMetadata(ctx, &group.MsgUpdateGroupMetadata{
					GroupId:  myGroupID,
					Admin:    addr1.String(),
					Metadata: []byte{1, 2, 3},
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"with policy modified": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Choice:     group.Choice_CHOICE_NO,
			},
			doBefore: func(ctx context.Context) {
				m, err := group.NewMsgUpdateGroupAccountDecisionPolicyRequest(
					addr1,
					groupAccount,
					&group.ThresholdDecisionPolicy{
						Threshold: "1",
						Timeout:   time.Duration(1),
					},
				)
				s.Require().NoError(err)

				_, err = s.msgClient.UpdateGroupAccountDecisionPolicy(ctx, m)
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx := s.ctx
			if !spec.srcCtx.IsZero() {
				sdkCtx = spec.srcCtx
			}
			sdkCtx, _ = sdkCtx.CacheContext()

			if spec.doBefore != nil {
				spec.doBefore(ctx.Context())
			}
			_, err := s.msgClient.Vote(ctx.Context(), spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			s.Require().NoError(err)
			// vote is stored and all data persisted
			res, err := s.queryClient.VoteByProposalVoter(ctx.Context(), &group.QueryVoteByProposalVoter{
				ProposalId: spec.req.ProposalId,
				Voter:      spec.req.Voter,
			})
			s.Require().NoError(err)
			loaded := res.Vote
			s.Assert().Equal(spec.req.ProposalId, loaded.ProposalId)
			s.Assert().Equal(spec.req.Voter, loaded.Voter)
			s.Assert().Equal(spec.req.Choice, loaded.Choice)
			s.Assert().Equal(spec.req.Metadata, loaded.Metadata)
			lsubmittedAt, err := gogotypes.TimestampProto(loaded.SubmittedAt)
			s.Require().NoError(err)
			submittedAt, err := gogotypes.TimestampFromProto(lsubmittedAt)
			s.Require().NoError(err)
			s.Assert().Equal(s.blockTime, submittedAt)

			// query votes by proposal
			votesByProposalRes, err := s.queryClient.VotesByProposal(ctx.Context(), &group.QueryVotesByProposal{
				ProposalId: spec.req.ProposalId,
			})
			s.Require().NoError(err)
			votesByProposal := votesByProposalRes.Votes
			s.Require().Equal(1, len(votesByProposal))
			vote := votesByProposal[0]
			s.Assert().Equal(spec.req.ProposalId, vote.ProposalId)
			s.Assert().Equal(spec.req.Voter, vote.Voter)
			s.Assert().Equal(spec.req.Choice, vote.Choice)
			s.Assert().Equal(spec.req.Metadata, vote.Metadata)
			vsubmittedAt, err := gogotypes.TimestampProto(vote.SubmittedAt)
			s.Require().NoError(err)
			submittedAt, err = gogotypes.TimestampFromProto(vsubmittedAt)
			s.Require().NoError(err)
			s.Assert().Equal(s.blockTime, submittedAt)

			// query votes by voter
			voter := spec.req.Voter
			votesByVoterRes, err := s.queryClient.VotesByVoter(ctx.Context(), &group.QueryVotesByVoter{
				Voter: voter,
			})
			s.Require().NoError(err)
			votesByVoter := votesByVoterRes.Votes
			s.Require().Equal(1, len(votesByVoter))
			s.Assert().Equal(spec.req.ProposalId, votesByVoter[0].ProposalId)
			s.Assert().Equal(voter, votesByVoter[0].Voter)
			s.Assert().Equal(spec.req.Choice, votesByVoter[0].Choice)
			s.Assert().Equal(spec.req.Metadata, votesByVoter[0].Metadata)
			vsubmittedAt, err = gogotypes.TimestampProto(votesByVoter[0].SubmittedAt)
			s.Require().NoError(err)
			submittedAt, err = gogotypes.TimestampFromProto(vsubmittedAt)
			s.Require().NoError(err)
			s.Assert().Equal(s.blockTime, submittedAt)

			// and proposal is updated
			proposalRes, err := s.queryClient.Proposal(ctx.Context(), &group.QueryProposal{
				ProposalId: spec.req.ProposalId,
			})
			s.Require().NoError(err)
			proposal := proposalRes.Proposal
			s.Assert().Equal(spec.expVoteState, proposal.VoteState)
			s.Assert().Equal(spec.expResult, proposal.Result)
			s.Assert().Equal(spec.expProposalStatus, proposal.Status)
			s.Assert().Equal(spec.expExecutorResult, proposal.ExecutorResult)

			spec.postRun(sdkCtx)
		})
	}
}

func (s *TestSuite) TestExecProposal() {
	ctx, addrs := s.ctx, s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]

	msgSend1 := &banktypes.MsgSend{
		FromAddress: s.groupAccountAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}
	msgSend2 := &banktypes.MsgSend{
		FromAddress: s.groupAccountAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 10001)},
	}
	proposers := []string{addr2.String()}

	specs := map[string]struct {
		srcBlockTime      time.Time
		setupProposal     func(ctx context.Context) uint64
		expErr            bool
		expProposalStatus group.Proposal_Status
		expProposalResult group.Proposal_Result
		expExecutorResult group.Proposal_ExecutorResult
		expFromBalances   sdk.Coins
		expToBalances     sdk.Coins
	}{
		"proposal executed when accepted": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_YES)
			},
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultSuccess,
			expFromBalances:   sdk.Coins{sdk.NewInt64Coin("test", 9800)},
			expToBalances:     sdk.Coins{sdk.NewInt64Coin("test", 200)},
		},

		"proposal with multiple messages executed when accepted": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1, msgSend1}
				return createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_YES)
			},
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultSuccess,
			expFromBalances:   sdk.Coins{sdk.NewInt64Coin("test", 9700)},
			expToBalances:     sdk.Coins{sdk.NewInt64Coin("test", 300)},
		},
		"proposal not executed when rejected": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_NO)
			},
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultRejected,
			expExecutorResult: group.ProposalExecutorResultNotRun,
		},
		"open proposal must not fail": {
			setupProposal: func(ctx context.Context) uint64 {
				return createProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)
			},
			expProposalStatus: group.ProposalStatusSubmitted,
			expProposalResult: group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
		},
		"existing proposal required": {
			setupProposal: func(ctx context.Context) uint64 {
				return 9999
			},
			expErr: true,
		},
		"Decision policy also applied on timeout": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_NO)
			},
			srcBlockTime:      s.blockTime.Add(time.Second),
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultRejected,
			expExecutorResult: group.ProposalExecutorResultNotRun,
		},
		"Decision policy also applied after timeout": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_NO)
			},
			srcBlockTime:      s.blockTime.Add(time.Second).Add(time.Millisecond),
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultRejected,
			expExecutorResult: group.ProposalExecutorResultNotRun,
		},
		"with group modified before tally": {
			setupProposal: func(ctx context.Context) uint64 {
				myProposalID := createProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)

				// then modify group
				_, err := s.msgClient.UpdateGroupMetadata(ctx, &group.MsgUpdateGroupMetadata{
					Admin:    addr1.String(),
					GroupId:  s.groupID,
					Metadata: []byte{1, 2, 3},
				})
				s.Require().NoError(err)
				return myProposalID
			},
			expProposalStatus: group.ProposalStatusAborted,
			expProposalResult: group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
		},
		"with group account modified before tally": {
			setupProposal: func(ctx context.Context) uint64 {
				myProposalID := createProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)
				_, err := s.msgClient.UpdateGroupAccountMetadata(ctx, &group.MsgUpdateGroupAccountMetadata{
					Admin:    addr1.String(),
					Address:  s.groupAccountAddr.String(),
					Metadata: []byte("group account modified before tally"),
				})
				s.Require().NoError(err)
				return myProposalID
			},
			expProposalStatus: group.ProposalStatusAborted,
			expProposalResult: group.ProposalResultUnfinalized,
			expExecutorResult: group.ProposalExecutorResultNotRun,
		},
		"prevent double execution when successful": {
			setupProposal: func(ctx context.Context) uint64 {
				myProposalID := createProposalAndVote(ctx, s, []sdk.Msg{msgSend1}, proposers, group.Choice_CHOICE_YES)

				_, err := s.msgClient.Exec(ctx, &group.MsgExec{Signer: addr1.String(), ProposalId: myProposalID})
				s.Require().NoError(err)
				return myProposalID
			},
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultSuccess,
			expFromBalances:   sdk.Coins{sdk.NewInt64Coin("test", 9800)},
			expToBalances:     sdk.Coins{sdk.NewInt64Coin("test", 200)},
		},
		"rollback all msg updates on failure": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1, msgSend2}
				return createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_YES)
			},
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultFailure,
		},
		"executable when failed before": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend2}
				myProposalID := createProposalAndVote(ctx, s, msgs, proposers, group.Choice_CHOICE_YES)

				_, err := s.msgClient.Exec(ctx, &group.MsgExec{Signer: addr1.String(), ProposalId: myProposalID})
				s.Require().NoError(err)
				s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, s.ctx, s.groupAccountAddr, sdk.Coins{sdk.NewInt64Coin("test", 10002)}))

				return myProposalID
			},
			expProposalStatus: group.ProposalStatusClosed,
			expProposalResult: group.ProposalResultAccepted,
			expExecutorResult: group.ProposalExecutorResultSuccess,
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx, _ := s.ctx.CacheContext()
			// ctx := types.Context{Context: sdkCtx}

			proposalID := spec.setupProposal(ctx.Context())

			if !spec.srcBlockTime.IsZero() {
				sdkCtx = sdkCtx.WithBlockTime(spec.srcBlockTime)
				// ctx = types.Context{Context: sdkCtx}
			}

			_, err := s.msgClient.Exec(ctx.Context(), &group.MsgExec{Signer: addr1.String(), ProposalId: proposalID})
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// and proposal is updated
			res, err := s.queryClient.Proposal(ctx.Context(), &group.QueryProposal{ProposalId: proposalID})
			s.Require().NoError(err)
			proposal := res.Proposal

			exp := group.Proposal_Result_name[int32(spec.expProposalResult)]
			got := group.Proposal_Result_name[int32(proposal.Result)]
			s.Assert().Equal(exp, got)

			exp = group.Proposal_Status_name[int32(spec.expProposalStatus)]
			got = group.Proposal_Status_name[int32(proposal.Status)]
			s.Assert().Equal(exp, got)

			exp = group.Proposal_ExecutorResult_name[int32(spec.expExecutorResult)]
			got = group.Proposal_ExecutorResult_name[int32(proposal.ExecutorResult)]
			s.Assert().Equal(exp, got)

			if spec.expFromBalances != nil {
				fromBalances := s.bankKeeper.GetAllBalances(sdkCtx, s.groupAccountAddr)
				s.Require().Equal(spec.expFromBalances, fromBalances)
			}
			if spec.expToBalances != nil {
				toBalances := s.bankKeeper.GetAllBalances(sdkCtx, addr2)
				s.Require().Equal(spec.expToBalances, toBalances)
			}
		})
	}
}

func createProposal(
	ctx context.Context, s *TestSuite, msgs []sdk.Msg,
	proposers []string) uint64 {
	proposalReq := &group.MsgCreateProposal{
		Address:   s.groupAccountAddr.String(),
		Proposers: proposers,
		Metadata:  nil,
	}
	err := proposalReq.SetMsgs(msgs)
	s.Require().NoError(err)

	proposalRes, err := s.msgClient.CreateProposal(ctx, proposalReq)
	s.Require().NoError(err)
	return proposalRes.ProposalId
}

func createProposalAndVote(
	ctx context.Context, s *TestSuite, msgs []sdk.Msg,
	proposers []string, choice group.Choice) uint64 {
	s.Require().Greater(len(proposers), 0)
	myProposalID := createProposal(ctx, s, msgs, proposers)

	_, err := s.msgClient.Vote(ctx, &group.MsgVote{
		ProposalId: myProposalID,
		Voter:      proposers[0],
		Choice:     choice,
	})
	s.Require().NoError(err)
	return myProposalID
}

func createGroupAndGroupAccount(
	admin sdk.AccAddress,
	s *TestSuite,
) (string, uint64, group.DecisionPolicy, []byte) {
	groupRes, err := s.msgClient.CreateGroup(s.ctx.Context(), &group.MsgCreateGroup{
		Admin:    admin.String(),
		Members:  nil,
		Metadata: nil,
	})
	s.Require().NoError(err)

	myGroupID := groupRes.GroupId
	groupAccount := &group.MsgCreateGroupAccount{
		Admin:    admin.String(),
		GroupId:  myGroupID,
		Metadata: nil,
	}

	policy := group.NewThresholdDecisionPolicy(
		"1",
		time.Duration(1),
	)
	err = groupAccount.SetDecisionPolicy(policy)
	s.Require().NoError(err)

	groupAccountRes, err := s.msgClient.CreateGroupAccount(s.ctx.Context(), groupAccount)
	s.Require().NoError(err)

	res, err := s.queryClient.GroupAccountInfo(s.ctx.Context(), &group.QueryGroupAccountInfo{Address: groupAccountRes.Address})
	s.Require().NoError(err)

	return groupAccountRes.Address, myGroupID, policy, res.Info.DerivationKey
}
