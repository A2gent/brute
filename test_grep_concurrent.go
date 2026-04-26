package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"regexp"
	"io/fs"
	"bytes"
	"bufio"
	"os"
	"io"
)
// just a stub to make sure we don't have syntax errors when writing
