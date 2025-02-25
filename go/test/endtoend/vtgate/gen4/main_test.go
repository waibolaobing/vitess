/*
Copyright 2021 The Vitess Authors.

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

package vtgate

import (
	"flag"
	"os"
	"testing"

	"vitess.io/vitess/go/vt/vtgate/planbuilder"

	"vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/test/endtoend/cluster"
)

var (
	clusterInstance  *cluster.LocalProcessCluster
	vtParams         mysql.ConnParams
	shardedKs        = "ks"
	unshardedKs      = "uks"
	Cell             = "test"
	shardedSchemaSQL = `create table t1(
	id bigint,
	col bigint,
	primary key(id)
) Engine=InnoDB;

create table t2(
	id bigint,
	tcol1 varchar(50),
	tcol2 varchar(50),
	primary key(id)
) Engine=InnoDB;

create table t3(
	id bigint,
	tcol1 varchar(50),
	tcol2 varchar(50),
	primary key(id)
) Engine=InnoDB;

create table user_region(
	id bigint,
	cola bigint,
	colb bigint,
	primary key(id)
) Engine=InnoDB;
`
	unshardedSchemaSQL = `create table u_a(
	id bigint,
	a bigint,
	primary key(id)
) Engine=InnoDB;

create table u_b(
	id bigint,
	b varchar(50),
	primary key(id)
) Engine=InnoDB;
`

	shardedVSchema = `
{
  "sharded": true,
  "vindexes": {
    "xxhash": {
      "type": "xxhash"
    },
    "regional_vdx": {
	  "type": "region_experimental",
	  "params": {
		"region_bytes": "1"
	  }
    }
  },
  "tables": {
    "t1": {
      "column_vindexes": [
        {
          "column": "id",
          "name": "xxhash"
        }
      ]
    },
    "t2": {
      "column_vindexes": [
        {
          "column": "id",
          "name": "xxhash"
        }
      ],
      "columns": [
        {
          "name": "tcol1",
          "type": "VARCHAR"
        }
      ]
    },
    "t3": {
      "column_vindexes": [
        {
          "column": "id",
          "name": "xxhash"
        }
      ],
      "columns": [
        {
          "name": "tcol1",
          "type": "VARCHAR"
        }
      ]
    },
    "user_region": {
	  "column_vindexes": [
	    {
          "columns": ["cola","colb"],
		  "name": "regional_vdx"
		}
      ]
	}
  }
}`

	unshardedVSchema = `
{
  "sharded": false,
  "tables": {
    "u_a": {},
    "u_b": {}
  }
}`

	routingRules = `
{"rules": [
  {
    "from_table": "ks.t1000",
	"to_tables": ["ks.t1"]
  }
]}
`
)

func TestMain(m *testing.M) {
	defer cluster.PanicHandler(nil)
	flag.Parse()

	exitCode := func() int {
		clusterInstance = cluster.NewCluster(Cell, "localhost")
		defer clusterInstance.Teardown()

		// Start topo server
		err := clusterInstance.StartTopo()
		if err != nil {
			return 1
		}

		// Start keyspace
		sKs := &cluster.Keyspace{
			Name:      shardedKs,
			SchemaSQL: shardedSchemaSQL,
			VSchema:   shardedVSchema,
		}
		err = clusterInstance.StartKeyspace(*sKs, []string{"-80", "80-"}, 0, false)
		if err != nil {
			return 1
		}

		uKs := &cluster.Keyspace{
			Name:      unshardedKs,
			SchemaSQL: unshardedSchemaSQL,
			VSchema:   unshardedVSchema,
		}
		err = clusterInstance.StartUnshardedKeyspace(*uKs, 0, false)
		if err != nil {
			return 1
		}

		// apply routing rules
		err = clusterInstance.VtctlclientProcess.ApplyRoutingRules(routingRules)
		if err != nil {
			return 1
		}

		err = clusterInstance.VtctlclientProcess.ExecuteCommand("RebuildVSchemaGraph")
		if err != nil {
			return 1
		}

		// Start vtgate
		clusterInstance.VtGatePlannerVersion = planbuilder.Gen4 // enable Gen4 planner.
		err = clusterInstance.StartVtgate()
		if err != nil {
			return 1
		}
		vtParams = mysql.ConnParams{
			Host: clusterInstance.Hostname,
			Port: clusterInstance.VtgateMySQLPort,
		}
		return m.Run()
	}()
	os.Exit(exitCode)
}
