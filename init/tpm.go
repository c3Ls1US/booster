package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

var defaultSymScheme = &tpm2.SymScheme{
	Alg:     tpm2.AlgAES,
	KeyBits: 128,
	Mode:    tpm2.AlgCFB,
}

var defaultRSAParams = &tpm2.RSAParams{
	Symmetric: defaultSymScheme,
	KeyBits:   2048,
}

var defaultECCParams = &tpm2.ECCParams{
	Symmetric: defaultSymScheme,
	CurveID:   tpm2.CurveNISTP256,
}

var enableSwEmulator bool

func openTPM() (io.ReadWriteCloser, error) {
	var dev io.ReadWriteCloser
	var err error

	if enableSwEmulator {
		dev, err = net.Dial("tcp", ":2321") // swtpm emulator is listening at port 2321
	} else {
		dev, err = tpm2.OpenTPM("/dev/tpmrm0")
	}
	if err != nil {
		return nil, err
	}

	if _, err := tpm2.GetManufacturer(dev); err != nil {
		return nil, fmt.Errorf("device is not a TPM 2.0")
	}

	return dev, nil
}

// Waits until a tpm device is available for use. Times out and returns false after 3 seconds.
func tpmAwaitReady() bool {
	timedOut := waitTimeout(&tpmReadyWg, time.Second*3)
	if timedOut {
		info("no tpm devices found after 3 seconds.")
	}
	return !timedOut
}

func tpm2Unseal(public, private []byte, pcrs []int, bank tpm2.Algorithm, policyHash, password []byte) ([]byte, error) {
	dev, err := openTPM()
	if err != nil {
		return nil, err
	}
	defer dev.Close()

	// TODO: the digest may need to be verified
	sessHandle, _, err := policyPCRSession(dev, pcrs, bank, policyHash, password != nil)
	if err != nil {
		return nil, err
	}
	defer tpm2.FlushContext(dev, sessHandle)

	// default srk template
	srkTemplate := tpm2.Public{
		Type:       tpm2.AlgRSA,
		NameAlg:    tpm2.AlgSHA256,
		Attributes: tpm2.FlagFixedTPM | tpm2.FlagFixedParent | tpm2.FlagSensitiveDataOrigin | tpm2.FlagUserWithAuth | tpm2.FlagRestricted | tpm2.FlagDecrypt | tpm2.FlagNoDA,
		AuthPolicy: nil,
		RSAParameters: &tpm2.RSAParams{
			Symmetric: &tpm2.SymScheme{
				Alg:     tpm2.AlgAES,
				KeyBits: 128,
				Mode:    tpm2.AlgCFB,
			},
			KeyBits:    2048,
			ModulusRaw: make([]byte, 256),
		},
	}

	srkHandle, _, err := tpm2.CreatePrimary(dev, tpm2.HandleOwner, tpm2.PCRSelection{}, "", "", srkTemplate)
	if err != nil {
		return nil, fmt.Errorf("clevis.go/tpm2: can't create primary key: %v", err)
	}
	defer tpm2.FlushContext(dev, srkHandle)

	// TODO: in addition to the srkHandle these public/private parts can be completely wrong as well
	objectHandle, _, err := tpm2.Load(dev, srkHandle, "", public, private)
	if err != nil {
		return nil, fmt.Errorf("clevis.go/tpm2: unable to load data: %v", err)
	}
	defer tpm2.FlushContext(dev, objectHandle)

	// before passing in the PIN, compute the hash then stringify
	// TODO: research an appropriate buffer size
	var buffer tpmutil.U16Bytes
	in := bytes.NewBuffer(password)
	if err := buffer.TPMUnmarshal(in); err != nil {
		return nil, fmt.Errorf("tpmutil: failed when unmarshalling buffer")
	}
	// assuming the digest is already truncated
	digest, _, err := tpm2.Hash(dev, bank, buffer, objectHandle)
	if err != nil {
		return nil, fmt.Errorf("tpm2: failed when computing hash for buffer")
	}

	unsealed, err := tpm2.UnsealWithSession(dev, sessHandle, objectHandle, string(digest))
	if err != nil {
		return nil, fmt.Errorf("unable to unseal data: %v", err)
	}
	console("Recovered unsealed data from tpm")

	return unsealed, nil
}

func parsePCRBank(bank string) tpm2.Algorithm {
	switch bank {
	case "sha1":
		return tpm2.AlgSHA1
	case "sha256":
		return tpm2.AlgSHA256
	}
	return tpm2.AlgSHA256
}

// Returns session handle and policy digest.
func policyPCRSession(dev io.ReadWriteCloser, pcrs []int, algo tpm2.Algorithm, expectedDigest []byte, usePassword bool) (handle tpmutil.Handle, policy []byte, retErr error) {
	// This session assumes the bus is trusted, so we:
	// - use nil for tpmkey, encrypted salt, and symmetric
	// - use and all-zeros caller nonce, and ignore the returned nonce
	// As we are creating a plain TPM session, we:
	// - setup a policy session
	// - don't bind the session to any particular key
	sessHandle, _, err := tpm2.StartAuthSession(
		dev,
		/*tpmkey=*/ tpm2.HandleNull,
		/*bindkey=*/ tpm2.HandleNull,
		/*nonceCaller=*/ make([]byte, 32),
		/*encryptedSalt=*/ nil,
		/*sessionType=*/ tpm2.SessionPolicy,
		/*symmetric=*/ tpm2.AlgNull,
		/*authHash=*/ tpm2.AlgSHA256)
	if err != nil {
		return tpm2.HandleNull, nil, fmt.Errorf("unable to start session: %v", err)
	}

	pcrSelection := tpm2.PCRSelection{
		Hash: algo,
		PCRs: pcrs,
	}

	// An empty expected digest means that digest verification is skipped.
	if err := tpm2.PolicyPCR(dev, sessHandle, nil, pcrSelection); err != nil {
		return tpm2.HandleNull, nil, fmt.Errorf("unable to bind PCRs to auth policy: %v", err)
	}

	if usePassword {
		if err := tpm2.PolicyPassword(dev, sessHandle); err != nil {
			return tpm2.HandleNull, nil, err
		}
	}

	policy, err = tpm2.PolicyGetDigest(dev, sessHandle)
	if err != nil {
		return tpm2.HandleNull, nil, fmt.Errorf("unable to get policy digest: %v", err)
	}

	if !bytes.Equal(policy, expectedDigest) {
		return tpm2.HandleNull, nil, fmt.Errorf("current policy digest does not match stored policy digest, cancelling TPM2 authentication attempt")
	}

	return sessHandle, policy, nil
}
