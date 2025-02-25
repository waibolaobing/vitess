/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tabletserver

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"vitess.io/vitess/go/vt/vttablet/tabletserver/tx"

	"context"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"vitess.io/vitess/go/mysql/fakesqldb"
	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/vtgate/fakerpcvtgateconn"
	"vitess.io/vitess/go/vt/vtgate/vtgateconn"
	"vitess.io/vitess/go/vt/vttablet/tabletserver/tabletenv"

	querypb "vitess.io/vitess/go/vt/proto/query"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

func TestTxExecutorEmptyPrepare(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTransaction(tsv, nil)
	err := txe.Prepare(txid, "aa")
	require.NoError(t, err)
	// Nothing should be prepared.
	require.Empty(t, txe.te.preparedPool.conns, "txe.te.preparedPool.conns")
}

func TestTxExecutorPrepare(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	err := txe.Prepare(txid, "aa")
	require.NoError(t, err)
	err = txe.RollbackPrepared("aa", 1)
	require.NoError(t, err)
	// A retry should still succeed.
	err = txe.RollbackPrepared("aa", 1)
	require.NoError(t, err)
	// A retry  with no original id should also succeed.
	err = txe.RollbackPrepared("aa", 0)
	require.NoError(t, err)
}

func TestTxExecutorPrepareNotInTx(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	err := txe.Prepare(0, "aa")
	require.EqualError(t, err, "transaction 0: not found")
}

func TestTxExecutorPreparePoolFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid1 := newTxForPrep(tsv)
	txid2 := newTxForPrep(tsv)
	err := txe.Prepare(txid1, "aa")
	require.NoError(t, err)
	defer txe.RollbackPrepared("aa", 0)
	err = txe.Prepare(txid2, "bb")
	require.Error(t, err)
	require.Contains(t, err.Error(), "prepared transactions exceeded limit")
}

func TestTxExecutorPrepareRedoBeginFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	db.AddRejectedQuery("begin", errors.New("begin fail"))
	err := txe.Prepare(txid, "aa")
	defer txe.RollbackPrepared("aa", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "begin fail")
}

func TestTxExecutorPrepareRedoFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	err := txe.Prepare(txid, "bb")
	defer txe.RollbackPrepared("bb", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not supported")
}

func TestTxExecutorPrepareRedoCommitFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	db.AddRejectedQuery("commit", errors.New("commit fail"))
	err := txe.Prepare(txid, "aa")
	defer txe.RollbackPrepared("aa", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "commit fail")
}

func TestTxExecutorCommit(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	err := txe.Prepare(txid, "aa")
	require.NoError(t, err)
	err = txe.CommitPrepared("aa")
	require.NoError(t, err)
	// Committing an absent transaction should succeed.
	err = txe.CommitPrepared("bb")
	require.NoError(t, err)
}

func TestTxExecutorCommitRedoFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	// Allow all additions to redo logs to succeed
	db.AddQueryPattern("insert into _vt\\.redo_state.*", &sqltypes.Result{})
	err := txe.Prepare(txid, "bb")
	require.NoError(t, err)
	defer txe.RollbackPrepared("bb", 0)
	db.AddQuery("update _vt.redo_state set state = 'Failed' where dtid = 'bb'", &sqltypes.Result{})
	err = txe.CommitPrepared("bb")
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not supported")
	// A retry should fail differently.
	err = txe.CommitPrepared("bb")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot commit dtid bb, state: failed")
}

func TestTxExecutorCommitRedoCommitFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	err := txe.Prepare(txid, "aa")
	require.NoError(t, err)
	defer txe.RollbackPrepared("aa", 0)
	db.AddRejectedQuery("commit", errors.New("commit fail"))
	err = txe.CommitPrepared("aa")
	require.Error(t, err)
	require.Contains(t, err.Error(), "commit fail")
}

func TestTxExecutorRollbackBeginFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	err := txe.Prepare(txid, "aa")
	require.NoError(t, err)
	db.AddRejectedQuery("begin", errors.New("begin fail"))
	err = txe.RollbackPrepared("aa", txid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "begin fail")
}

func TestTxExecutorRollbackRedoFail(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	txid := newTxForPrep(tsv)
	// Allow all additions to redo logs to succeed
	db.AddQueryPattern("insert into _vt\\.redo_state.*", &sqltypes.Result{})
	err := txe.Prepare(txid, "bb")
	require.NoError(t, err)
	err = txe.RollbackPrepared("bb", txid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not supported")
}

func TestExecutorCreateTransaction(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	db.AddQueryPattern(fmt.Sprintf("insert into _vt\\.dt_state\\(dtid, state, time_created\\) values \\('aa', %d,.*", int(querypb.TransactionState_PREPARE)), &sqltypes.Result{})
	db.AddQueryPattern("insert into _vt\\.dt_participant\\(dtid, id, keyspace, shard\\) values \\('aa', 1,.*", &sqltypes.Result{})
	err := txe.CreateTransaction("aa", []*querypb.Target{{
		Keyspace: "t1",
		Shard:    "0",
	}})
	require.NoError(t, err)
}

func TestExecutorStartCommit(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	commitTransition := fmt.Sprintf("update _vt.dt_state set state = %d where dtid = 'aa' and state = %d", int(querypb.TransactionState_COMMIT), int(querypb.TransactionState_PREPARE))
	db.AddQuery(commitTransition, &sqltypes.Result{RowsAffected: 1})
	txid := newTxForPrep(tsv)
	err := txe.StartCommit(txid, "aa")
	require.NoError(t, err)

	db.AddQuery(commitTransition, &sqltypes.Result{})
	txid = newTxForPrep(tsv)
	err = txe.StartCommit(txid, "aa")
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not transition to COMMIT: aa")
}

func TestExecutorSetRollback(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	rollbackTransition := fmt.Sprintf("update _vt.dt_state set state = %d where dtid = 'aa' and state = %d", int(querypb.TransactionState_ROLLBACK), int(querypb.TransactionState_PREPARE))
	db.AddQuery(rollbackTransition, &sqltypes.Result{RowsAffected: 1})
	txid := newTxForPrep(tsv)
	err := txe.SetRollback("aa", txid)
	require.NoError(t, err)

	db.AddQuery(rollbackTransition, &sqltypes.Result{})
	txid = newTxForPrep(tsv)
	err = txe.SetRollback("aa", txid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not transition to ROLLBACK: aa")
}

func TestExecutorConcludeTransaction(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	db.AddQuery("delete from _vt.dt_state where dtid = 'aa'", &sqltypes.Result{})
	db.AddQuery("delete from _vt.dt_participant where dtid = 'aa'", &sqltypes.Result{})
	err := txe.ConcludeTransaction("aa")
	require.NoError(t, err)
}

func TestExecutorReadTransaction(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	db.AddQuery("select dtid, state, time_created from _vt.dt_state where dtid = 'aa'", &sqltypes.Result{})
	got, err := txe.ReadTransaction("aa")
	require.NoError(t, err)
	want := &querypb.TransactionMetadata{}
	if !proto.Equal(got, want) {
		t.Errorf("ReadTransaction: %v, want %v", got, want)
	}

	txResult := &sqltypes.Result{
		Fields: []*querypb.Field{
			{Type: sqltypes.VarChar},
			{Type: sqltypes.Int64},
			{Type: sqltypes.Int64},
		},
		Rows: [][]sqltypes.Value{{
			sqltypes.NewVarBinary("aa"),
			sqltypes.NewInt64(int64(querypb.TransactionState_PREPARE)),
			sqltypes.NewVarBinary("1"),
		}},
	}
	db.AddQuery("select dtid, state, time_created from _vt.dt_state where dtid = 'aa'", txResult)
	db.AddQuery("select keyspace, shard from _vt.dt_participant where dtid = 'aa'", &sqltypes.Result{
		Fields: []*querypb.Field{
			{Type: sqltypes.VarChar},
			{Type: sqltypes.VarChar},
		},
		Rows: [][]sqltypes.Value{{
			sqltypes.NewVarBinary("test1"),
			sqltypes.NewVarBinary("0"),
		}, {
			sqltypes.NewVarBinary("test2"),
			sqltypes.NewVarBinary("1"),
		}},
	})
	got, err = txe.ReadTransaction("aa")
	require.NoError(t, err)
	want = &querypb.TransactionMetadata{
		Dtid:        "aa",
		State:       querypb.TransactionState_PREPARE,
		TimeCreated: 1,
		Participants: []*querypb.Target{{
			Keyspace:   "test1",
			Shard:      "0",
			TabletType: topodatapb.TabletType_PRIMARY,
		}, {
			Keyspace:   "test2",
			Shard:      "1",
			TabletType: topodatapb.TabletType_PRIMARY,
		}},
	}
	if !proto.Equal(got, want) {
		t.Errorf("ReadTransaction: %v, want %v", got, want)
	}

	txResult = &sqltypes.Result{
		Fields: []*querypb.Field{
			{Type: sqltypes.VarChar},
			{Type: sqltypes.Int64},
			{Type: sqltypes.Int64},
		},
		Rows: [][]sqltypes.Value{{
			sqltypes.NewVarBinary("aa"),
			sqltypes.NewInt64(int64(querypb.TransactionState_COMMIT)),
			sqltypes.NewVarBinary("1"),
		}},
	}
	db.AddQuery("select dtid, state, time_created from _vt.dt_state where dtid = 'aa'", txResult)
	want.State = querypb.TransactionState_COMMIT
	got, err = txe.ReadTransaction("aa")
	require.NoError(t, err)
	if !proto.Equal(got, want) {
		t.Errorf("ReadTransaction: %v, want %v", got, want)
	}

	txResult = &sqltypes.Result{
		Fields: []*querypb.Field{
			{Type: sqltypes.VarChar},
			{Type: sqltypes.Int64},
			{Type: sqltypes.Int64},
		},
		Rows: [][]sqltypes.Value{{
			sqltypes.NewVarBinary("aa"),
			sqltypes.NewInt64(int64(querypb.TransactionState_ROLLBACK)),
			sqltypes.NewVarBinary("1"),
		}},
	}
	db.AddQuery("select dtid, state, time_created from _vt.dt_state where dtid = 'aa'", txResult)
	want.State = querypb.TransactionState_ROLLBACK
	got, err = txe.ReadTransaction("aa")
	require.NoError(t, err)
	if !proto.Equal(got, want) {
		t.Errorf("ReadTransaction: %v, want %v", got, want)
	}
}

func TestExecutorReadAllTransactions(t *testing.T) {
	txe, tsv, db := newTestTxExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	db.AddQuery(txe.te.twoPC.readAllTransactions, &sqltypes.Result{
		Fields: []*querypb.Field{
			{Type: sqltypes.VarChar},
			{Type: sqltypes.Int64},
			{Type: sqltypes.Int64},
			{Type: sqltypes.VarChar},
			{Type: sqltypes.VarChar},
		},
		Rows: [][]sqltypes.Value{{
			sqltypes.NewVarBinary("dtid0"),
			sqltypes.NewInt64(int64(querypb.TransactionState_PREPARE)),
			sqltypes.NewVarBinary("1"),
			sqltypes.NewVarBinary("ks01"),
			sqltypes.NewVarBinary("shard01"),
		}},
	})
	got, _, _, err := txe.ReadTwopcInflight()
	require.NoError(t, err)
	want := []*tx.DistributedTx{{
		Dtid:    "dtid0",
		State:   "PREPARE",
		Created: time.Unix(0, 1),
		Participants: []querypb.Target{{
			Keyspace: "ks01",
			Shard:    "shard01",
		}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadAllTransactions:\n%s, want\n%s", jsonStr(got), jsonStr(want))
	}
}

// These vars and types are used only for TestExecutorResolveTransaction
var dtidCh = make(chan string)

type FakeVTGateConn struct {
	fakerpcvtgateconn.FakeVTGateConn
}

func (conn *FakeVTGateConn) ResolveTransaction(ctx context.Context, dtid string) error {
	dtidCh <- dtid
	return nil
}

func TestExecutorResolveTransaction(t *testing.T) {
	protocol := "resolveTest"
	var save string
	save, *vtgateconn.VtgateProtocol = *vtgateconn.VtgateProtocol, protocol
	defer func() { *vtgateconn.VtgateProtocol = save }()

	vtgateconn.RegisterDialer(protocol, func(context.Context, string) (vtgateconn.Impl, error) {
		return &FakeVTGateConn{
			FakeVTGateConn: fakerpcvtgateconn.FakeVTGateConn{},
		}, nil
	})
	_, tsv, db := newShortAgeExecutor(t)
	defer db.Close()
	defer tsv.StopService()
	want := "aa"
	db.AddQueryPattern(
		"select dtid, time_created from _vt\\.dt_state where time_created.*",
		&sqltypes.Result{
			Fields: []*querypb.Field{
				{Type: sqltypes.VarChar},
				{Type: sqltypes.Int64},
			},
			Rows: [][]sqltypes.Value{{
				sqltypes.NewVarBinary(want),
				sqltypes.NewVarBinary("1"),
			}},
		})
	got := <-dtidCh
	if got != want {
		t.Errorf("ResolveTransaction: %s, want %s", got, want)
	}
}

func TestNoTwopc(t *testing.T) {
	txe, tsv, db := newNoTwopcExecutor(t)
	defer db.Close()
	defer tsv.StopService()

	testcases := []struct {
		desc string
		fun  func() error
	}{{
		desc: "Prepare",
		fun:  func() error { return txe.Prepare(1, "aa") },
	}, {
		desc: "CommitPrepared",
		fun:  func() error { return txe.CommitPrepared("aa") },
	}, {
		desc: "RollbackPrepared",
		fun:  func() error { return txe.RollbackPrepared("aa", 1) },
	}, {
		desc: "CreateTransaction",
		fun:  func() error { return txe.CreateTransaction("aa", nil) },
	}, {
		desc: "StartCommit",
		fun:  func() error { return txe.StartCommit(1, "aa") },
	}, {
		desc: "SetRollback",
		fun:  func() error { return txe.SetRollback("aa", 1) },
	}, {
		desc: "ConcludeTransaction",
		fun:  func() error { return txe.ConcludeTransaction("aa") },
	}, {
		desc: "ReadTransaction",
		fun: func() error {
			_, err := txe.ReadTransaction("aa")
			return err
		},
	}, {
		desc: "ReadAllTransactions",
		fun: func() error {
			_, _, _, err := txe.ReadTwopcInflight()
			return err
		},
	}}

	want := "2pc is not enabled"
	for _, tc := range testcases {
		err := tc.fun()
		require.EqualError(t, err, want)
	}
}

func newTestTxExecutor(t *testing.T) (txe *TxExecutor, tsv *TabletServer, db *fakesqldb.DB) {
	db = setUpQueryExecutorTest(t)
	logStats := tabletenv.NewLogStats(ctx, "TestTxExecutor")
	tsv = newTestTabletServer(ctx, smallTxPool, db)
	db.AddQueryPattern("insert into _vt\\.redo_state\\(dtid, state, time_created\\) values \\('aa', 1,.*", &sqltypes.Result{})
	db.AddQueryPattern("insert into _vt\\.redo_statement.*", &sqltypes.Result{})
	db.AddQuery("delete from _vt.redo_state where dtid = 'aa'", &sqltypes.Result{})
	db.AddQuery("delete from _vt.redo_statement where dtid = 'aa'", &sqltypes.Result{})
	db.AddQuery("update test_table set `name` = 2 where pk = 1 limit 10001", &sqltypes.Result{})
	return &TxExecutor{
		ctx:      ctx,
		logStats: logStats,
		te:       tsv.te,
	}, tsv, db
}

// newShortAgeExecutor is same as newTestTxExecutor, but shorter transaction abandon age.
func newShortAgeExecutor(t *testing.T) (txe *TxExecutor, tsv *TabletServer, db *fakesqldb.DB) {
	db = setUpQueryExecutorTest(t)
	logStats := tabletenv.NewLogStats(ctx, "TestTxExecutor")
	tsv = newTestTabletServer(ctx, smallTxPool|shortTwopcAge, db)
	db.AddQueryPattern("insert into _vt\\.redo_state\\(dtid, state, time_created\\) values \\('aa', 1,.*", &sqltypes.Result{})
	db.AddQueryPattern("insert into _vt\\.redo_statement.*", &sqltypes.Result{})
	db.AddQuery("delete from _vt.redo_state where dtid = 'aa'", &sqltypes.Result{})
	db.AddQuery("delete from _vt.redo_statement where dtid = 'aa'", &sqltypes.Result{})
	db.AddQuery("update test_table set `name` = 2 where pk = 1 limit 10001", &sqltypes.Result{})
	return &TxExecutor{
		ctx:      ctx,
		logStats: logStats,
		te:       tsv.te,
	}, tsv, db
}

// newNoTwopcExecutor is same as newTestTxExecutor, but 2pc disabled.
func newNoTwopcExecutor(t *testing.T) (txe *TxExecutor, tsv *TabletServer, db *fakesqldb.DB) {
	db = setUpQueryExecutorTest(t)
	logStats := tabletenv.NewLogStats(ctx, "TestTxExecutor")
	tsv = newTestTabletServer(ctx, noTwopc, db)
	return &TxExecutor{
		ctx:      ctx,
		logStats: logStats,
		te:       tsv.te,
	}, tsv, db
}

// newTxForPrep creates a non-empty transaction.
func newTxForPrep(tsv *TabletServer) int64 {
	txid := newTransaction(tsv, nil)
	target := querypb.Target{TabletType: topodatapb.TabletType_PRIMARY}
	_, err := tsv.Execute(ctx, &target, "update test_table set name = 2 where pk = 1", nil, txid, 0, nil)
	if err != nil {
		panic(err)
	}
	return txid
}
