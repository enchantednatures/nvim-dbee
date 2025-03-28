package adapters

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/arangodb/go-driver/v2/arangodb"

	"github.com/kndndrj/nvim-dbee/dbee/core"
	"github.com/kndndrj/nvim-dbee/dbee/core/builders"
)

var (
	_ core.Driver           = (*arangoDriver)(nil)
	_ core.DatabaseSwitcher = (*arangoDriver)(nil)
)

type arangoDriver struct {
	c      arangodb.Client
	dbName string
}

func (a *arangoDriver) ListDatabases() (current string, available []string, err error) {
	if a == nil || a.c == nil {
		return "", nil, errors.New("arangoDriver is not initialized")
	}

	log.Print("Fetching list of databases")
	databases, err := a.c.AccessibleDatabases(context.Background())
	if err != nil {
		return "", nil, fmt.Errorf("failed to list databases: %w", err)
	}

	databaseNames := make([]string, len(databases))
	for i, db := range databases {
		databaseNames[i] = db.Name()
	}

	return a.dbName, databaseNames, nil
}

func (a *arangoDriver) SelectDatabase(name string) error {
	if a == nil {
		return errors.New("arangoDriver is not initialized")
	}
	log.Printf("Selecting database: %s", name)
	a.dbName = name
	return nil
}

func (a *arangoDriver) Close() {
	log.Print("Closing connection (not yet implemented)")
}

func (a *arangoDriver) Columns(opts *core.TableOptions) ([]*core.Column, error) {
	if a == nil || a.c == nil {
		return nil, errors.New("arangoDriver is not initialized")
	}
	if a.dbName == "" {
		return nil, errors.New("database not selected")
	}

	log.Printf("Fetching columns for table: %s", opts.Table)

	db, err := a.c.GetDatabase(context.Background(), a.dbName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get database: %w", err)
	}

	aql := `
	FOR n IN @@col
		LIMIT 1000
		FOR a IN ATTRIBUTES(n)
		COLLECT attribute = a WITH COUNT INTO len
		SORT len DESC
		LIMIT 10
		sort attribute
		RETURN {attribute}`

	bindVars := map[string]interface{}{"@col": opts.Table}
	cursor, err := db.Query(context.Background(), aql, &arangodb.QueryOptions{BindVars: bindVars})
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer cursor.Close()

	columns := []*core.Column{}
	var doc map[string]any

	for cursor.HasMore() {
		_, err := cursor.ReadDocument(context.Background(), &doc)
		if err != nil {
			return nil, fmt.Errorf("failed to read document: %w", err)
		}
		column := fmt.Sprintf("%s", doc["attribute"])
		columns = append(columns, &core.Column{Type: "collection", Name: column})
	}

	return columns, nil
}

func (a *arangoDriver) Query(ctx context.Context, query string) (core.ResultStream, error) {
	if a == nil || a.c == nil {
		return nil, errors.New("arangoDriver is not initialized")
	}
	if a.dbName == "" {
		return nil, errors.New("database not selected")
	}

	log.Printf("Executing query: %s", query)
	db, err := a.c.GetDatabase(ctx, a.dbName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get database: %w", err)
	}

	cursor, err := db.Query(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer cursor.Close()
	next, hasNext := builders.NextNil()

	next, hasNext = builders.NextYield(func(yield func(...any)) error {
		if !cursor.HasMore() {
			next, hasNext = builders.NextNil()
		}

		for {
			if !cursor.HasMore() {
				break
			}
			var doc interface{}

			_, err = cursor.ReadDocument(ctx, &doc)
			if err != nil {
				return err
			}

			yield(NewArangoResponse(doc))
		}

		return nil
	})

	return builders.NewResultStreamBuilder().
		WithNextFunc(next, hasNext).
		WithHeader(core.Header{"Results"}).
		WithMeta(&core.Meta{SchemaType: core.SchemaLess}).
		Build(), nil
}

func (a *arangoDriver) Structure() ([]*core.Structure, error) {
	if a == nil || a.c == nil {
		return nil, errors.New("arangoDriver is not initialized")
	}

	ctx := context.Background()
	databases, err := a.c.Databases(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list databases: %w", err)
	}

	structures := make([]*core.Structure, len(databases))
	for i, db := range databases {
		collections, err := db.Collections(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list collections: %w", err)
		}

		structures[i] = &core.Structure{
			Name:     db.Name(),
			Schema:   db.Name(),
			Type:     core.StructureTypeSchema,
			Children: make([]*core.Structure, len(collections)),
		}

		for j, collection := range collections {
			structures[i].Children[j] = &core.Structure{
				Name:   collection.Name(),
				Schema: db.Name(),
				Type:   core.StructureTypeTable,
			}
		}
	}
	return structures, nil
}

type arangoResponse struct {
	Value any
}

func NewArangoResponse(val any) any {
	return &arangoResponse{Value: val}
}

func (ar *arangoResponse) String() string {
	parsed, err := json.MarshalIndent(ar.Value, "", "  ")
	if err != nil {
		return fmt.Sprint(ar.Value)
	}
	return string(parsed)
}

func (ar *arangoResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(ar.Value)
}

func (ar *arangoResponse) GobEncode() ([]byte, error) {
	var err error
	w := new(bytes.Buffer)
	encoder := gob.NewEncoder(w)
	err = encoder.Encode(ar.Value)
	if err != nil {
		return nil, err
	}
	return w.Bytes(), err
}

func (ar *arangoResponse) GobDecode(buf []byte) error {
	var err error
	r := bytes.NewBuffer(buf)
	decoder := gob.NewDecoder(r)
	err = decoder.Decode(&ar.Value)
	if err != nil {
		return err
	}
	return err
}
