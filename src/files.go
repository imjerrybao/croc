package croc

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"

	log "github.com/cihub/seelog"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

func (c *Croc) processFile(src string) (err error) {
	fd := FileMetaData{}

	// pathToFile and filename are the files that should be used internally
	var pathToFile, filename string
	// first check if it is stdin
	if src == "stdin" {
		var f *os.File
		f, err = ioutil.TempFile(".", "croc-stdin-")
		if err != nil {
			return
		}
		_, err = io.Copy(f, os.Stdin)
		if err != nil {
			return
		}
		pathToFile = "."
		filename = f.Name()
		err = f.Close()
		if err != nil {
			return
		}
		// fd.Name is what the user will see
		fd.Name = "stdin"
		fd.DeleteAfterSending = true
	} else {
		pathToFile, filename = filepath.Split(filepath.Clean(src))
		fd.Name = filename
	}

	// check wether the file is a dir
	info, err := os.Stat(path.Join(pathToFile, filename))
	if err != nil {
		return
	}
	fd.IsDir = info.Mode().IsDir()

	// zip file
	c.crocFile, err = zipFile(path.Join(pathToFile, filename), c.UseCompression)
	fd.IsCompressed = c.UseCompression

	fd.Hash, err = hashFile(c.crocFile)
	if err != nil {
		log.Error(err)
		return err
	}
	fd.Size, err = fileSize(c.crocFile)
	if err != nil {
		err = errors.Wrap(err, "could not determine filesize")
		log.Error(err)
		return err
	}

	c.cs.Lock()
	defer c.cs.Unlock()
	c.cs.channel.fileMetaData = fd
	return
}

func (c *Croc) getFilesReady(ws *websocket.Conn) (err error) {
	c.cs.Lock()
	defer c.cs.Unlock()
	log.Debug("getting files ready")
	c.cs.channel.notSentMetaData = true
	// send metadata

	// wait until data is ready
	for {
		if c.cs.channel.fileMetaData.Name != "" {
			break
		}
		c.cs.Unlock()
		time.Sleep(10 * time.Millisecond)
		c.cs.Lock()
	}

	// get passphrase
	var passphrase []byte
	passphrase, err = c.cs.channel.Pake.SessionKey()
	if err != nil {
		return
	}
	// encrypt file data
	// create temporary filename
	var f *os.File
	f, err = ioutil.TempFile(".", "croc-encrypted")
	if err != nil {
		return
	}
	c.crocFileEncrypted = f.Name()
	f.Close()
	os.Remove(c.crocFileEncrypted)
	err = encryptFile(c.crocFile, c.crocFileEncrypted, passphrase)
	if err != nil {
		return
	}
	// remove the unencrypted versoin
	if err = os.Remove(c.crocFile); err != nil {
		return
	}
	c.cs.channel.fileMetaData.IsEncrypted = true
	// split into pieces to send
	log.Debugf("splitting %s", c.crocFileEncrypted)
	if err = splitFile(c.crocFileEncrypted, len(c.cs.channel.Ports)); err != nil {
		return
	}
	// remove the file now since we still have pieces
	if err = os.Remove(c.crocFileEncrypted); err != nil {
		return
	}

	// encrypt meta data
	var metaDataBytes []byte
	metaDataBytes, err = json.Marshal(c.cs.channel.fileMetaData)
	if err != nil {
		return
	}
	c.cs.channel.EncryptedFileMetaData = encrypt(metaDataBytes, passphrase)

	c.cs.channel.Update = true
	log.Debugf("updating channel")
	errWrite := ws.WriteJSON(c.cs.channel)
	if errWrite != nil {
		log.Error(errWrite)
	}
	c.cs.channel.Update = false
	go func() {
		// encrypt the files
		// TODO
		c.cs.Lock()
		c.cs.channel.fileReady = true
		c.cs.Unlock()
	}()
	return
}
