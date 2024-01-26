package postman

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/trufflesecurity/trufflehog/v3/pkg/context"

	"github.com/go-errors/errors"
	"github.com/go-logr/logr"
	"github.com/trufflesecurity/trufflehog/v3/pkg/common"
	"github.com/trufflesecurity/trufflehog/v3/pkg/pb/source_metadatapb"
	"github.com/trufflesecurity/trufflehog/v3/pkg/pb/sourcespb"
	"github.com/trufflesecurity/trufflehog/v3/pkg/sources"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	SourceType    = sourcespb.SourceType_SOURCE_TYPE_POSTMAN
	LINK_BASE_URL = "https://go.postman.co/"
)

type Source struct {
	name        string
	sourceId    sources.SourceID
	jobId       sources.JobID
	verify      bool
	concurrency int
	log         logr.Logger
	sources.Progress
	jobPool *errgroup.Group
	client  *Client
	conn    *sourcespb.Postman
	sources.CommonSourceUnitUnmarshaller
}

//ToDo: Update this to match the proto file

type PMScanObject struct {
	Link            string
	WorkspaceUUID   string
	WorkspaceName   string
	GlobalID        string
	CollectionID    string
	CollectionName  string
	EnvironmentID   string
	EnvironmentName string
	RequestID       string
	RequestName     string
	FieldType       string
	FieldName       string
	VarType         string
	Data            string
}

type ArchiveJSON struct {
	Collection  map[string]bool `json:"collection"`
	Environment map[string]bool `json:"environment"`
}

func IsValidUUID(u string) bool {
	_, err := uuid.Parse(u)
	return err == nil
}

func verifyPostmanExportZip(filepath string) bool {
	// Open the ZIP archive.
	r, err := zip.OpenReader(filepath)
	if err != nil {
		fmt.Println("Error opening ZIP file:", err)
		return false
	}
	defer r.Close()

	// Iterate through the files in the ZIP archive.
	for _, file := range r.File {
		if file.Name == "archive.json" {
			// Open the file within the ZIP archive.
			rc, err := file.Open()
			if err != nil {
				fmt.Println("Error opening archive.json:", err)
				return false
			}
			defer rc.Close()

			// Read the contents of archive.json.
			contents, err := io.ReadAll(rc)
			if err != nil {
				fmt.Println("Error reading archive.json:", err)
				return false
			}

			// Unmarshal the JSON contents into the ArchiveJSON struct.
			var archiveData ArchiveJSON
			if err := json.Unmarshal(contents, &archiveData); err != nil {
				fmt.Println("Error decoding JSON:", err)
				return false
			}

			// Check if the structure matches your requirements.
			return archiveData.Collection != nil || archiveData.Environment != nil
		}
	}
	return false
}

// Type returns the type of source.
// It is used for matching source types in configuration and job input.
func (s *Source) Type() sourcespb.SourceType {
	return SourceType
}

func (s *Source) SourceID() sources.SourceID {
	return s.sourceId
}

func (s *Source) JobID() sources.JobID {
	return s.jobId
}

// Init returns an initialized Postman source.
func (s *Source) Init(ctx context.Context, name string, jobId sources.JobID, sourceId sources.SourceID, verify bool, connection *anypb.Any, concurrency int) error {
	s.name = name
	s.sourceId = sourceId
	s.jobId = jobId
	s.verify = verify
	s.jobPool = &errgroup.Group{}
	s.jobPool.SetLimit(concurrency)

	var conn sourcespb.Postman
	if err := anypb.UnmarshalTo(connection, &conn, proto.UnmarshalOptions{}); err != nil {
		return errors.WrapPrefix(err, "error unmarshalling connection", 0)
	}

	s.conn = &conn

	switch conn.Credential.(type) {
	case *sourcespb.Postman_Token:
		if conn.GetToken() == "" {
			return errors.New("Postman token is empty")
		}
		s.client = NewClient(conn.GetToken())
		s.client.HTTPClient = common.RetryableHttpClientTimeout(3)

		// I think we should check access to the workspace

		//Consider adding an auth check here. But even if token is valid, doesn't mean we have access to the target source. So unnecssary?
	case *sourcespb.Postman_Unauthenticated:
		s.client = nil
		// No client needed if reading from local
	default:
		return errors.New("credential type not implemented for Postman")
	}
	return nil
}

func (s *Source) Chunks(ctx context.Context, chunksChan chan *sources.Chunk, _ ...sources.ChunkingTarget) error {
	// Prep all of the objects to scan. Then scan them.
	if s.conn.Workspaces != nil {
		for _, workspace := range s.conn.Workspaces {
			s.scanWorkspace(ctx, chunksChan, workspace)
		}
	}
	return nil

	// if s.conn.Collection != "" {
	// 	//s.scanCollection(ctx, chunksChan)
	// 	// Filter out collections

	// }
	// if s.conn.Environment != "" {
	// 	//s.scanEnvironment(ctx, chunksChan)
	// 	// Filter out environments

	// }

}

func (s *Source) scanWorkspace(ctx context.Context, chunksChan chan *sources.Chunk, wrkspc string) error {
	logger := ctx.Logger().WithValues("workspace", wrkspc)
	if IsValidUUID(wrkspc) {
		// User provided a valid UUID for interaction with API
		workspace, err := s.client.GetWorkspace(wrkspc)
		if err != nil {
			s.log.Error(err, "could not get workspace object", "workspace_uuid", wrkspc)
		}
		logger.V(2).Info("scanning workspace")

		varSubMap := []map[string]string{}
		extraKeywords := []string{workspace.Workspace.Name}
		s.scanGlobalVars(ctx, chunksChan, wrkspc, extraKeywords, &varSubMap)
	} else {
		// Check if user provided a valid Postman export zip file
		isWorkspace := verifyPostmanExportZip(wrkspc)
		if !isWorkspace {
			logger.Error(errors.New("invalid workspace"), "invalid workspace", "workspace", wrkspc)
			return errors.New("invalid workspace filepath")
		}
		// No Global Vars to scan when providing a local file
	}
	// Get all collections in workspace
	// Filter out collections
	//scanCollection(ctx, chunksChan)
	// Get all environments in workspace
	// Filter out enviroments
	// Get all globals in workspace
	// Scan workspace name
	return nil
}

func (s *Source) scanGlobalVars(ctx context.Context, chunksChan chan *sources.Chunk, workspaceUUID string, extraKeywords []string, varSubMap *[]map[string]string) {
	// Might also want to add long string where the other keys and values are nearby.
	globalVars, err := s.client.GetGlobals(workspaceUUID)
	if err != nil {
		s.log.Error(err, "could not get global variables object", "workspace_uuid", workspaceUUID)
	}
	ctx.Logger().V(2).Info("starting scanning global variables")

	// Map key=value pairs for subsitution later in workspace processing
	varSubstitutions := map[string]string{}
	// Create slice of objects to scan (both context & data)
	pmObjToScan := []PMScanObject{}
	for _, globalVar := range globalVars.Data.Values {
		key := globalVar.Key
		value := globalVar.Value
		variableType := globalVar.Type
		// Add to map for substitution later
		varSubstitutions[key] = fmt.Sprintf("%v", value)
		// Create scan object
		preScanObj := PMScanObject{
			Link:          LINK_BASE_URL + globalVars.Data.ID,
			WorkspaceUUID: workspaceUUID,
			GlobalID:      globalVars.Data.ID,
			FieldType:     "Global Variable",
			FieldName:     key,
			VarType:       variableType,
			Data:          fmt.Sprintf("%s:%v\n", key, value),
		}
		pmObjToScan = append(pmObjToScan, preScanObj)
		// For each extra keyword, sub in keywords as key for key=value pair
		for _, keyword := range extraKeywords {
			preScanObj.Data = fmt.Sprintf("%s:%v\n", keyword, value)
			pmObjToScan = append(pmObjToScan, preScanObj)
		}

		// This is a legacy field from Postman. But they can still exist (although invisible in UI).
		if globalVar.SessionValue != "" {
			// In rare cases where sessionValue isn't empty, we'll overwrite with the substitution value.
			varSubstitutions[key] = fmt.Sprintf("%v", globalVar.SessionValue)
			// Create scan object and add to slice
			preScanObj.Data = fmt.Sprintf("%s:%v\n", key, globalVar.SessionValue)
			pmObjToScan = append(pmObjToScan, preScanObj)
			for _, keyword := range extraKeywords {
				// Same process as above for keywords
				preScanObj.Data = fmt.Sprintf("%s:%v\n", keyword, globalVar.SessionValue)
				pmObjToScan = append(pmObjToScan, preScanObj)
			}
		}
	}
	// Add to slice of maps for substitution later
	*varSubMap = append(*varSubMap, varSubstitutions)
	// Process each object.
	done := make(chan struct{})

	// Process each object concurrently.
	for _, obj := range pmObjToScan {
		go func(obj PMScanObject) {
			defer func() {
				done <- struct{}{} // Signal that the goroutine has completed.
			}()
			s.scanObject(ctx, chunksChan, obj)
		}(obj)
	}

	// Wait for all goroutines to finish.
	for range pmObjToScan {
		<-done
	}
	ctx.Logger().V(2).Info("finished scanning global variables")
}

func (s *Source) scanObject(ctx context.Context, chunksChan chan *sources.Chunk, o PMScanObject) {
	fmt.Println(o)
	chunksChan <- &sources.Chunk{
		SourceType: s.Type(),
		SourceName: s.name,
		SourceID:   s.SourceID(),
		JobID:      s.JobID(),
		Data:       []byte(o.Data),
		SourceMetadata: &source_metadatapb.MetaData{
			Data: &source_metadatapb.MetaData_Postman{
				Postman: &source_metadatapb.Postman{
					Link:            o.Link,
					WorkspaceUuid:   o.WorkspaceUUID,
					WorkspaceName:   o.WorkspaceName,
					CollectionId:    o.CollectionID,
					CollectionName:  o.CollectionName,
					EnvironmentId:   o.EnvironmentID,
					EnvironmentName: o.EnvironmentName,
					RequestId:       o.RequestID,
					RequestName:     o.RequestName,
					FieldType:       o.FieldType,
					FieldName:       o.FieldName,
					VariableType:    o.VarType,
				},
			},
		},
		Verify: s.verify,
	}
}

// func (s *Source) scanCollection(ctx context.Context, chunksChan chan *sources.Chunk) {
// 	// Filter out collcetions
// }

// func (s *Source) scanEnvironment(ctx context.Context, chunksChan chan *sources.Chunk) {
// 	// Filter out environments
// }

// func (s *Source) collectionChunker(ctx context.Context, chunksChan chan *sources.Chunk, collections []string, errorCount *sync.Map, objectCount *uint64) {
// 	for _, collection := range collections {

// 		if common.IsDone(ctx) {
// 			return
// 		}

// 		if collection == nil {
// 			continue
// 		}

// 		s.jobPool.Go(func() error {
// 			defer common.RecoverWithExit(ctx)

// 			if strings.HasSuffix(*obj.Key, "/") {
// 				s.log.V(5).Info("Skipping directory", "object", *obj.Key)
// 				return nil
// 			}

// 			path := strings.Split(*obj.Key, "/")
// 			prefix := strings.Join(path[:len(path)-1], "/")

// 			nErr, ok := errorCount.Load(prefix)
// 			if !ok {
// 				nErr = 0
// 			}
// 			if nErr.(int) > 3 {
// 				s.log.V(2).Info("Skipped due to excessive errors", "object", *obj.Key)
// 				return nil
// 			}

// 			// files break with spaces, must replace with +
// 			// objKey := strings.ReplaceAll(*obj.Key, " ", "+")
// 			ctx, cancel := context.WithTimeout(ctx, time.Second*5)
// 			defer cancel()
// 			res, err := client.GetObjectWithContext(ctx, &s3.GetObjectInput{
// 				Bucket: &bucket,
// 				Key:    obj.Key,
// 			})
// 			if err != nil {
// 				if !strings.Contains(err.Error(), "AccessDenied") {
// 					s.log.Error(err, "could not get S3 object", "object", *obj.Key)
// 				}

// 				nErr, ok := errorCount.Load(prefix)
// 				if !ok {
// 					nErr = 0
// 				}
// 				if nErr.(int) > 3 {
// 					s.log.V(3).Info("Skipped due to excessive errors", "object", *obj.Key)
// 					return nil
// 				}
// 				nErr = nErr.(int) + 1
// 				errorCount.Store(prefix, nErr)
// 				// too many consective errors on this page
// 				if nErr.(int) > 3 {
// 					s.log.V(2).Info("Too many consecutive errors, excluding prefix", "prefix", prefix)
// 				}
// 				return nil
// 			}

// 			bufferName := cleantemp.MkFilename()

// 			defer res.Body.Close()
// 			reader, err := diskbufferreader.New(res.Body, diskbufferreader.WithBufferName(bufferName))
// 			if err != nil {
// 				s.log.Error(err, "Could not create reader.")
// 				return nil
// 			}
// 			defer reader.Close()

// 			email := "Unknown"
// 			if obj.Owner != nil {
// 				email = *obj.Owner.DisplayName
// 			}
// 			modified := obj.LastModified.String()
// 			chunkSkel := &sources.Chunk{
// 				SourceType: s.Type(),
// 				SourceName: s.name,
// 				SourceID:   s.SourceID(),
// 				JobID:      s.JobID(),
// 				SourceMetadata: &source_metadatapb.MetaData{
// 					Data: &source_metadatapb.MetaData_S3{
// 						S3: &source_metadatapb.S3{
// 							Bucket:    bucket,
// 							File:      sanitizer.UTF8(*obj.Key),
// 							Link:      sanitizer.UTF8(makeS3Link(bucket, *client.Config.Region, *obj.Key)),
// 							Email:     sanitizer.UTF8(email),
// 							Timestamp: sanitizer.UTF8(modified),
// 						},
// 					},
// 				},
// 				Verify: s.verify,
// 			}
// 			if handlers.HandleFile(ctx, reader, chunkSkel, sources.ChanReporter{Ch: chunksChan}) {
// 				atomic.AddUint64(objectCount, 1)
// 				s.log.V(5).Info("S3 object scanned.", "object_count", objectCount, "page_number", pageNumber)
// 				return nil
// 			}

// 			if err := reader.Reset(); err != nil {
// 				s.log.Error(err, "Error resetting reader to start.")
// 			}
// 			reader.Stop()

// 			chunkReader := sources.NewChunkReader()
// 			chunkResChan := chunkReader(ctx, reader)
// 			for data := range chunkResChan {
// 				if err := data.Error(); err != nil {
// 					s.log.Error(err, "error reading chunk.")
// 					continue
// 				}
// 				chunk := *chunkSkel
// 				chunk.Data = data.Bytes()
// 				if err := common.CancellableWrite(ctx, chunksChan, &chunk); err != nil {
// 					return err
// 				}
// 			}

// 			atomic.AddUint64(objectCount, 1)
// 			s.log.V(5).Info("S3 object scanned.", "object_count", objectCount, "page_number", pageNumber)
// 			nErr, ok = errorCount.Load(prefix)
// 			if !ok {
// 				nErr = 0
// 			}
// 			if nErr.(int) > 0 {
// 				errorCount.Store(prefix, 0)
// 			}

// 			return nil
// 		})
// 	}

// 	_ = s.jobPool.Wait()
// }

// // postmanChunker()

// //maybe a process object function that takes a chunk and processes it?

// // Need a function to get globals and process them, and then teh same for the rest
