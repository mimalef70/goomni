// Command providercatalog exports the same finite native contracts used by
// admission. Documentation generation never needs live credentials or clients.
package main

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/infrastructure/providers/eitaa"
	"github.com/mimalef70/goomni/src/infrastructure/providers/rubika"
)

func main() {
	catalogs := map[domains.Provider][]domains.OperationContract{}
	for _, contract := range []domains.ProviderContract{bale.Contract{}, eitaa.Contract{}, rubika.Contract{}} {
		list := contract.Operations()
		sort.Slice(list, func(i, j int) bool { return list[i].Operation < list[j].Operation })
		catalogs[contract.Descriptor().ID] = list
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(catalogs); err != nil {
		panic(err)
	}
}
