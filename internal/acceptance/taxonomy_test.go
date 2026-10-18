//go:build integration

package acceptance

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taxonomyCategory struct {
	ID       string  `json:"id"`
	Code     string  `json:"code"`
	ParentID *string `json:"parent_id"`
	NameCS   string  `json:"name_cs"`
	NameSK   string  `json:"name_sk"`
	NameEN   string  `json:"name_en"`
	Kind     string  `json:"kind"`
	ColorKey *string `json:"color_key"`
	Custom   bool    `json:"custom"`
	Usage    *struct {
		Transactions int `json:"transactions"`
		Rules        int `json:"rules"`
	} `json:"usage"`
}

func (c *client) taxonomy() map[string]taxonomyCategory {
	c.t.Helper()
	var out struct {
		Categories []taxonomyCategory `json:"categories"`
	}
	c.do(http.MethodGet, "/api/v1/categories", nil, http.StatusOK, &out)
	byID := map[string]taxonomyCategory{}
	for _, cat := range out.Categories {
		byID[cat.ID] = cat
	}
	return byID
}

// createCategory removes the category again when the test ends: TruncateAll keeps the taxonomy.
func (c *client) createCategory(body map[string]any) taxonomyCategory {
	c.t.Helper()
	var created taxonomyCategory
	c.do(http.MethodPost, "/api/v1/categories", body, http.StatusCreated, &created)
	c.t.Cleanup(func() {
		if _, ok := c.taxonomy()[created.ID]; ok {
			c.do(http.MethodDelete, "/api/v1/categories/"+created.ID+"?merge_into="+c.categoryID("other"), nil, http.StatusNoContent, nil)
		}
	})
	return created
}

func TestCategoriesCanBeAddedWithReadableCodes(t *testing.T) {
	c := newClient(t)
	pets := c.createCategory(map[string]any{"name": "Domácí mazlíčci",
		"kind": "expense", "color_key": "limeta", "icon": "dog"})
	assert.Equal(t, "domaci_mazlicci", pets.Code)
	assert.Equal(t, "Domácí mazlíčci", pets.NameSK, "a user's own category has one name in every locale")
	assert.Equal(t, "Domácí mazlíčci", pets.NameEN)
	assert.Equal(t, "Housing and utilities", c.taxonomy()[c.categoryID("housing")].NameEN)

	var renamed taxonomyCategory
	c.do(http.MethodPatch, "/api/v1/categories/"+pets.ID, map[string]any{"name": "Zvířata"}, http.StatusOK, &renamed)
	assert.Equal(t, "Zvířata", renamed.NameCS)
	assert.Equal(t, "Zvířata", renamed.NameSK)
	assert.Nil(t, pets.ParentID)

	food := c.createCategory(map[string]any{"parent_id": pets.ID, "name": "Krmivo",
		"kind": "income", "color_key": "malina"})
	assert.Equal(t, "domaci_mazlicci.krmivo", food.Code)
	assert.Equal(t, "expense", food.Kind, "a subcategory takes the parent's kind")
	assert.Nil(t, food.ColorKey, "a subcategory takes the parent's colour")

	again := c.createCategory(map[string]any{"name": "Domácí mazlíčci", "kind": "expense"})
	assert.Equal(t, "domaci_mazlicci-2", again.Code)

	c.do(http.MethodPost, "/api/v1/categories", map[string]any{"name": "Bez typu"},
		http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPost, "/api/v1/categories", map[string]any{"parent_id": food.ID, "name": "Vnuk"},
		http.StatusUnprocessableEntity, nil)
}

func TestSubcategoryMovesUnderAnotherMainCategory(t *testing.T) {
	c := newClient(t)
	pets := c.createCategory(map[string]any{"name": "Mazlíčci", "kind": "expense"})
	vet := c.createCategory(map[string]any{"parent_id": pets.ID, "name": "Veterinář"})
	health := c.categoryID("health")

	var moved taxonomyCategory
	c.do(http.MethodPatch, "/api/v1/categories/"+vet.ID, map[string]any{"parent_id": health}, http.StatusOK, &moved)
	require.NotNil(t, moved.ParentID)
	assert.Equal(t, health, *moved.ParentID)

	c.do(http.MethodPatch, "/api/v1/categories/"+vet.ID, map[string]any{"parent_id": vet.ID}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPatch, "/api/v1/categories/"+pets.ID, map[string]any{"parent_id": health}, http.StatusUnprocessableEntity, nil)
}

func TestMainCategoryKindChangeReachesSubcategories(t *testing.T) {
	c := newClient(t)
	side := c.createCategory(map[string]any{"name": "Přivýdělek", "kind": "expense"})
	gigs := c.createCategory(map[string]any{"parent_id": side.ID, "name": "Zakázky"})

	c.do(http.MethodPatch, "/api/v1/categories/"+side.ID, map[string]any{"kind": "income"}, http.StatusOK, nil)
	assert.Equal(t, "income", c.taxonomy()[gigs.ID].Kind)
}

func TestUsedCategoryIsDeletedOnlyByMergingIt(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	shopping := c.categoryID("shopping")
	gadgets := c.createCategory(map[string]any{"parent_id": shopping, "name": "Elektronika"})

	txs := c.transactions("")
	var shop, split string
	for _, tx := range txs.Transactions {
		switch tx.Counterparty {
		case "OBCHOD BEZ KATEGORIE":
			shop = tx.ID
		case "ROHLIK.CZ":
			split = tx.ID
		}
	}
	require.NotEmpty(t, shop)
	require.NotEmpty(t, split)
	c.do(http.MethodPut, "/api/v1/transactions/"+shop+"/categories", map[string]any{
		"lines": []map[string]any{{"category_id": gadgets.ID, "amount_minor": -30000}}, "save_rule": "counterparty",
	}, http.StatusOK, nil)
	c.do(http.MethodPut, "/api/v1/transactions/"+split+"/categories", map[string]any{"lines": []map[string]any{
		{"category_id": gadgets.ID, "amount_minor": -24900}, {"category_id": shopping, "amount_minor": -100000},
	}}, http.StatusOK, nil)
	c.do(http.MethodPut, "/api/v1/budgets/2026-10", map[string]any{"lines": []map[string]any{
		{"category_id": gadgets.ID, "planned_minor": 1000}, {"category_id": shopping, "planned_minor": 5000},
	}}, http.StatusOK, nil)

	usage := c.taxonomy()[gadgets.ID].Usage
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.Transactions)
	assert.Equal(t, 1, usage.Rules)

	c.do(http.MethodDelete, "/api/v1/categories/"+gadgets.ID, nil, http.StatusConflict, nil)
	c.do(http.MethodDelete, "/api/v1/categories/"+gadgets.ID+"?merge_into="+gadgets.ID, nil, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodDelete, "/api/v1/categories/"+gadgets.ID+"?merge_into="+shopping, nil, http.StatusNoContent, nil)

	_, exists := c.taxonomy()[gadgets.ID]
	assert.False(t, exists)
	for _, tx := range c.transactions("").Transactions {
		if tx.ID == shop || tx.ID == split {
			require.Len(t, tx.Categories, 1, "split lines in the same category merge")
			assert.Equal(t, shopping, tx.Categories[0].CategoryID)
		}
	}
	var rules struct {
		Rules []struct {
			CategoryID string `json:"category_id"`
		} `json:"rules"`
	}
	c.do(http.MethodGet, "/api/v1/rules", nil, http.StatusOK, &rules)
	require.Len(t, rules.Rules, 1)
	assert.Equal(t, shopping, rules.Rules[0].CategoryID)

	var budget struct {
		Lines []struct {
			CategoryID   string `json:"category_id"`
			PlannedMinor int64  `json:"planned_minor"`
		} `json:"lines"`
	}
	c.do(http.MethodGet, "/api/v1/budgets/2026-10", nil, http.StatusOK, &budget)
	require.Len(t, budget.Lines, 1)
	assert.Equal(t, int64(6000), budget.Lines[0].PlannedMinor)
}

func TestDeletingMainCategoryHandsItsSubcategoriesOver(t *testing.T) {
	c := newClient(t)
	hobby := c.createCategory(map[string]any{"name": "Koníčky", "kind": "expense"})
	music := c.createCategory(map[string]any{"parent_id": hobby.ID, "name": "Hudba"})
	leisure := c.categoryID("leisure")

	c.do(http.MethodDelete, "/api/v1/categories/"+hobby.ID, nil, http.StatusConflict, nil)
	c.do(http.MethodDelete, "/api/v1/categories/"+hobby.ID+"?merge_into="+music.ID, nil, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodDelete, "/api/v1/categories/"+hobby.ID+"?merge_into="+leisure, nil, http.StatusNoContent, nil)

	moved := c.taxonomy()[music.ID]
	require.NotNil(t, moved.ParentID)
	assert.Equal(t, leisure, *moved.ParentID)

	unused := c.createCategory(map[string]any{"name": "Prázdná", "kind": "expense"})
	c.do(http.MethodDelete, "/api/v1/categories/"+unused.ID, nil, http.StatusNoContent, nil)
}

func TestSystemCategoryOnlyTakesTheUsersLook(t *testing.T) {
	c := newClient(t)
	dining := c.categoryID("dining")

	c.do(http.MethodPatch, "/api/v1/categories/"+dining, map[string]any{"name": "Hospody"}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPatch, "/api/v1/categories/"+dining, map[string]any{"kind": "income"}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPatch, "/api/v1/categories/"+c.categoryID("dining.cafes"), map[string]any{"color_key": "kakao"},
		http.StatusUnprocessableEntity, nil)
	c.do(http.MethodDelete, "/api/v1/categories/"+dining+"?merge_into="+c.categoryID("other"), nil, http.StatusUnprocessableEntity, nil)

	var recoloured taxonomyCategory
	c.do(http.MethodPatch, "/api/v1/categories/"+dining, map[string]any{"color_key": "kakao", "icon": "coffee"}, http.StatusOK, &recoloured)
	require.NotNil(t, recoloured.ColorKey)
	assert.Equal(t, "kakao", *recoloured.ColorKey)
	assert.Equal(t, "Jídlo a pití venku", recoloured.NameCS)
	assert.False(t, recoloured.Custom)

	var systemColour string
	require.NoError(t, pg.Pool.QueryRow(t.Context(), `SELECT color_key FROM category WHERE id = $1`, dining).Scan(&systemColour))
	assert.Equal(t, "malina", systemColour, "the system row stays as shipped")

	own := c.createCategory(map[string]any{"parent_id": dining, "name": "Rozvoz"})
	assert.True(t, own.Custom)
}
