// Copyright 2017 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params/confp/tconvert"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/types/goethereum"
)

// makeGenesis creates a new genesis struct based on some user input.
func (w *wizard) makeGenesis() {
	// Construct a default genesis block
	genesis := &genesisT.Genesis{
		Timestamp:  uint64(time.Now().Unix()),
		GasLimit:   4700000,
		Difficulty: big.NewInt(524288),
		Alloc:      make(genesisT.GenesisAlloc),
		Config: &goethereum.ChainConfig{
			HomesteadBlock:      big.NewInt(0),
			EIP150Block:         big.NewInt(0),
			EIP155Block:         big.NewInt(0),
			EIP158Block:         big.NewInt(0),
			ByzantiumBlock:      big.NewInt(0),
			ConstantinopleBlock: big.NewInt(0),
			PetersburgBlock:     big.NewInt(0),
			IstanbulBlock:       big.NewInt(0),
		},
	}
	// Figure out which consensus engine to choose
	fmt.Println()
	fmt.Println("Which consensus engine to use? (default = clique)")
	fmt.Println(" 1. Ethash - proof-of-work")
	fmt.Println(" 2. Clique - proof-of-authority")

	choice := w.read()
	switch {
	case choice == "1":
		// In case of ethash, we're pretty much done
		genesis.Config.MustSetConsensusEngineType(ctypes.ConsensusEngineT_Ethash)
		genesis.ExtraData = make([]byte, 32)

	case choice == "" || choice == "2":
		// In the case of clique, configure the consensus parameters
		genesis.Difficulty = big.NewInt(1)
		genesis.Config.MustSetConsensusEngineType(ctypes.ConsensusEngineT_Clique)
		genesis.Config.SetCliquePeriod(15)
		genesis.Config.SetCliqueEpoch(30000)
		fmt.Println()
		fmt.Println("How many seconds should blocks take? (default = 15)")
		if err := genesis.Config.SetCliquePeriod(uint64(w.readDefaultInt(15))); err != nil {
			log.Crit("error setting clique period", "err", err)
			return
		}

		// We also need the initial list of signers
		fmt.Println()
		fmt.Println("Which accounts are allowed to seal? (mandatory at least one)")

		var signers []common.Address
		for {
			if address := w.readAddress(); address != nil {
				signers = append(signers, *address)
				continue
			}
			if len(signers) > 0 {
				break
			}
		}
		// Sort the signers and embed into the extra-data section
		for i := 0; i < len(signers); i++ {
			for j := i + 1; j < len(signers); j++ {
				if bytes.Compare(signers[i][:], signers[j][:]) > 0 {
					signers[i], signers[j] = signers[j], signers[i]
				}
			}
		}
		genesis.ExtraData = make([]byte, 32+len(signers)*common.AddressLength+65)
		for i, signer := range signers {
			copy(genesis.ExtraData[32+i*common.AddressLength:], signer[:])
		}

	default:
		log.Crit("Invalid consensus engine choice", "choice", choice)
	}
	// Consensus all set, just ask for initial funds and go
	fmt.Println()
	fmt.Println("Which accounts should be pre-funded? (advisable at least one)")
	for {
		// Read the address of the account to fund
		if address := w.readAddress(); address != nil {
			genesis.Alloc[*address] = genesisT.GenesisAccount{
				Balance: new(big.Int).Lsh(big.NewInt(1), 256-7), // 2^256 / 128 (allow many pre-funds without balance overflows)
			}
			continue
		}
		break
	}
	fmt.Println()
	fmt.Println("Should the precompile-addresses (0x1 .. 0xff) be pre-funded with 1 wei? (advisable yes)")
	if w.readDefaultYesNo(true) {
		// Add a batch of precompile balances to avoid them getting deleted
		for i := int64(0); i < 256; i++ {
			genesis.Alloc[common.BigToAddress(big.NewInt(i))] = genesisT.GenesisAccount{Balance: big.NewInt(1)}
		}
	}
	// Query the user for some custom extras
	fmt.Println()
	fmt.Println("Specify your chain/network ID if you want an explicit one (default = random)")
	genesis.Config.SetChainID(new(big.Int).SetUint64(uint64(w.readDefaultInt(rand.Intn(65536)))))

	// All done, store the genesis and flush to disk
	log.Info("Configured new genesis block")

	w.conf.Genesis = genesis
	w.conf.flush()
}

// importGenesis imports a Geth genesis spec into puppeth.
func (w *wizard) importGenesis() {
	// Request the genesis JSON spec URL from the user
	fmt.Println()
	fmt.Println("Where's the genesis file? (local file or http/https url)")
	url := w.readURL()

	// Convert the various allowed URLs to a reader stream
	var reader io.Reader

	switch url.Scheme {
	case "http", "https":
		// Remote web URL, retrieve it via an HTTP client
		res, err := http.Get(url.String())
		if err != nil {
			log.Error("Failed to retrieve remote genesis", "err", err)
			return
		}
		defer res.Body.Close()
		reader = res.Body

	case "":
		// Schemaless URL, interpret as a local file
		file, err := os.Open(url.String())
		if err != nil {
			log.Error("Failed to open local genesis", "err", err)
			return
		}
		defer file.Close()
		reader = file

	default:
		log.Error("Unsupported genesis URL scheme", "scheme", url.Scheme)
		return
	}
	// Parse the genesis file and inject it successful
	var genesis genesisT.Genesis
	if err := json.NewDecoder(reader).Decode(&genesis); err != nil {
		log.Error("Invalid genesis spec", "err", err)
		return
	}
	log.Info("Imported genesis block")

	w.conf.Genesis = &genesis
	w.conf.flush()
}

// manageGenesis permits the modification of chain configuration parameters in
// a genesis config and the export of the entire genesis spec.
func (w *wizard) manageGenesis() {
	// Figure out whether to modify or export the genesis
	fmt.Println()
	fmt.Println(" 1. Modify existing configurations")
	fmt.Println(" 2. Export genesis configurations")
	fmt.Println(" 3. Remove genesis configuration")

	// sanitizeUint64P safeguards against taking the value of a potential nil address.
	// In case a nil is passed, the max integer value is returned, which in the chain configuration context
	// is functionally equivalent to being unset.
	sanitizeUint64P := func(n *uint64) uint64 {
		if n == nil {
			return math.MaxUint64
		}
		return *n
	}

	choice := w.read()
	switch choice {
	case "1":
		// Fork rule updating requested, iterate over each fork
		fmt.Println()
		fmt.Printf("Which block should Homestead come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEthashHomesteadTransition()))
		w.conf.Genesis.Config.SetEthashHomesteadTransition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEthashHomesteadTransition())))

		fmt.Println()
		fmt.Printf("Which block should EIP150 (Tangerine Whistle) come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEIP150Transition()))
		w.conf.Genesis.Config.SetEIP150Transition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEIP150Transition())))

		fmt.Println()
		fmt.Printf("Which block should EIP155 (Spurious Dragon) come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEIP155Transition()))
		w.conf.Genesis.Config.SetEIP155Transition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEIP155Transition())))

		fmt.Println()
		fmt.Printf("Which block should EIP158/161 (also Spurious Dragon) come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEIP161dTransition()))
		w.conf.Genesis.Config.SetEIP161dTransition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEIP161dTransition())))

		fmt.Println()
		fmt.Printf("Which block should Byzantium come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEthashEIP649Transition()))
		w.conf.Genesis.Config.SetEthashEIP649Transition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEthashEIP649Transition())))

		fmt.Println()
		fmt.Printf("Which block should Constantinople come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEthashEIP1234Transition()))
		w.conf.Genesis.Config.SetEthashEIP1234Transition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEthashEIP1234Transition())))
		if w.conf.Genesis.Config.GetEIP1283DisableTransition() == nil {
			w.conf.Genesis.Config.SetEIP1283DisableTransition(w.conf.Genesis.Config.GetEIP1283DisableTransition())
		}
		fmt.Println()
		fmt.Printf("Which block should Petersburg come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEIP1283DisableTransition()))
		w.conf.Genesis.Config.SetEIP1283DisableTransition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEIP1283DisableTransition())))

		fmt.Println()
		fmt.Printf("Which block should Istanbul come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEIP145Transition()))
		w.conf.Genesis.Config.SetEIP145Transition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEIP145Transition())))

		fmt.Println()
		fmt.Printf("Which block should YOLOv2 come into effect? (default = %v)\n", sanitizeUint64P(w.conf.Genesis.Config.GetEIP2929Transition()))
		w.conf.Genesis.Config.SetEIP2929Transition(w.readDefaultUint64P(sanitizeUint64P(w.conf.Genesis.Config.GetEIP2929Transition())))

		out, _ := json.MarshalIndent(w.conf.Genesis.Config, "", "  ")
		fmt.Printf("Chain configuration updated:\n\n%s\n", out)

		w.conf.flush()

	case "2":
		// Save whatever genesis configuration we currently have
		fmt.Println()
		fmt.Printf("Which folder to save the genesis specs into? (default = current)\n")
		fmt.Printf("  Will create %s.json, %s-aleth.json, %s-harmony.json, %s-parity.json\n", w.network, w.network, w.network, w.network)

		folder := w.readDefaultString(".")
		if err := os.MkdirAll(folder, 0755); err != nil {
			log.Error("Failed to create spec folder", "folder", folder, "err", err)
			return
		}
		out, _ := json.MarshalIndent(w.conf.Genesis, "", "  ")

		// Export the native genesis spec used by puppeth and Geth
		gethJson := filepath.Join(folder, fmt.Sprintf("%s.json", w.network))
		if err := ioutil.WriteFile((gethJson), out, 0644); err != nil {
			log.Error("Failed to save genesis file", "err", err)
			return
		}
		log.Info("Saved native genesis chain spec", "path", gethJson)

		// Export the genesis spec used by Aleth (formerly C++ Ethereum)
		if spec, err := tconvert.NewAlethGenesisSpec(w.network, w.conf.Genesis); err != nil {
			log.Error("Failed to create Aleth chain spec", "err", err)
		} else {
			saveGenesis(folder, w.network, "aleth", spec)
		}
		// Export the genesis spec used by Parity
		if spec, err := tconvert.NewParityChainSpec(w.network, w.conf.Genesis, []string{}); err != nil {
			log.Error("Failed to create Parity chain spec", "err", err)
		} else {
			saveGenesis(folder, w.network, "parity", spec)
		}
		// Export the genesis spec used by Harmony (formerly EthereumJ)
		saveGenesis(folder, w.network, "harmony", w.conf.Genesis)

	case "3":
		// Make sure we don't have any services running
		if len(w.conf.servers()) > 0 {
			log.Error("Genesis reset requires all services and servers torn down")
			return
		}
		log.Info("Genesis block destroyed")

		w.conf.Genesis = nil
		w.conf.flush()
	default:
		log.Error("That's not something I can do")
		return
	}
}

// saveGenesis JSON encodes an arbitrary genesis spec into a pre-defined file.
func saveGenesis(folder, network, client string, spec interface{}) {
	path := filepath.Join(folder, fmt.Sprintf("%s-%s.json", network, client))

	out, _ := json.MarshalIndent(spec, "", "  ")
	if err := ioutil.WriteFile(path, out, 0644); err != nil {
		log.Error("Failed to save genesis file", "client", client, "err", err)
		return
	}
	log.Info("Saved genesis chain spec", "client", client, "path", path)
}
