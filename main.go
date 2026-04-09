package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/generative-ai-go/genai"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// ==========================================
// 1. MODELOS DE BASE DE DATOS (DBML -> GORM)
// ==========================================

type Formulario struct {
	IDFormulario      uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	Gender            string    `gorm:"type:varchar(6)"`
	Edad              int
	Peso              float64 `gorm:"type:numeric(5,2)"`
	Altura            int
	NivelActividad    string    `gorm:"type:varchar(20)"`
	Cuello            float64   `gorm:"type:numeric(5,2)"`
	Cintura           float64   `gorm:"type:numeric(5,2)"`
	Cadera            float64   `gorm:"type:numeric(5,2)"`
	Meta              string    `gorm:"type:varchar(15)"`
	VelocidadKgSemana float64   `gorm:"type:numeric(3,2)"`
	FechaRegistro     time.Time `gorm:"type:date"`
}

type Macro struct {
	IDMacro      uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	IDFormulario uuid.UUID `gorm:"type:uuid"`
	Calories     int
	Protein      int
	Carbs        int
	Fat          int
	Fiber        int
	Water        float64 `gorm:"type:numeric(4,2)"`
}

type DailyTrack struct {
	IDDayliTrack  uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	IDMacro       uuid.UUID `gorm:"type:uuid"`
	CaloriesCount int
	Protein       int
	Carbs         int
	Fat           int
	Fiber         int
	Water         int
	DateTrack     time.Time `gorm:"type:date"`
}

type FoodLog struct {
	IDFoodLog    uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	IDDayliTrack uuid.UUID `gorm:"type:uuid"`
	Food         string    `gorm:"type:text"`
	Calories     int
	Protein      int
	Carbs        int
	Fat          int
	Fiber        int
}

type GeminiNutritionResponse struct {
	Food     string `json:"food"`
	Calories int    `json:"calories"`
	Protein  int    `json:"protein"`
	Carbs    int    `json:"carbs"`
	Fat      int    `json:"fat"`
	Fiber    int    `json:"fiber"`
}

type WaterRequest struct {
	Amount int `json:"amount"`
}

var (
	globalLastReq time.Time
	lockMutex     sync.Mutex
)

func isRateLimited() bool {
	lockMutex.Lock()
	defer lockMutex.Unlock()
	if !globalLastReq.IsZero() && time.Since(globalLastReq) < 20*time.Second {
		return true
	}

	// Si pasó el filtro, guardamos la hora actual y dejamos pasar
	globalLastReq = time.Now()
	return false
}

// ==========================================
// 2. LÓGICA DE CÁLCULO (NUTRICIÓN)
// ==========================================
func calcularRequerimientos(f Formulario) Macro {
	var tmb float64
	if f.Gender == "Hombre" {
		tmb = (10 * f.Peso) + (6.25 * float64(f.Altura)) - (5 * float64(f.Edad)) + 5
	} else {
		tmb = (10 * f.Peso) + (6.25 * float64(f.Altura)) - (5 * float64(f.Edad)) - 161
	}

	multiplicadores := map[string]float64{
		"sedentario": 1.2,
		"ligero":     1.375,
		"moderado":   1.55,
		"activo":     1.725,
		"muy_activo": 1.9,
	}
	tdee := tmb * multiplicadores[f.NivelActividad]

	ajusteDiario := f.VelocidadKgSemana * 1100
	caloriasMeta := tdee
	if f.Meta == "bajar" {
		caloriasMeta -= ajusteDiario
	} else if f.Meta == "aumentar" {
		caloriasMeta += ajusteDiario
	}

	proteina := (caloriasMeta * 0.30) / 4
	carbs := (caloriasMeta * 0.45) / 4
	grasas := (caloriasMeta * 0.25) / 9
	fibra := (caloriasMeta / 1000) * 14

	var mlPorKg float64
	if f.Edad <= 17 {
		mlPorKg = 40
	} else if f.Edad <= 55 {
		mlPorKg = 35
	} else if f.Edad <= 65 {
		mlPorKg = 30
	} else {
		mlPorKg = 25
	}
	aguaLitros := (f.Peso * mlPorKg) / 1000.0

	return Macro{
		Calories: int(caloriasMeta),
		Protein:  int(proteina),
		Carbs:    int(carbs),
		Fat:      int(grasas),
		Fiber:    int(fibra),
		Water:    aguaLitros,
	}
}

// ==========================================
// 3. CONEXIÓN A BASE DATOS
// ==========================================
func initDB() *gorm.DB {

	dsn := "host=" + os.Getenv("PGHOST") + " user=" + os.Getenv("PGUSER") + " password=" + os.Getenv("PGPASSWORD") + " dbname=" + os.Getenv("PGDATABASE") + " port=5432 sslmode=" + os.Getenv("PGSSLMODE") + " channel_binding=" + os.Getenv("PGCHANNELBINDING")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal(" Error conectando a la BD:", err)
	}
	db.AutoMigrate(&Formulario{}, &Macro{}, &DailyTrack{}, &FoodLog{})
	log.Println(" Tablas sincronizadas")
	return db
}

// ==========================================
// 4. RUTAS DEL API (GIN)
// ==========================================
func main() {
	godotenv.Load()
	db := initDB()
	r := gin.Default()

	// CORS Middleware básico (importante para que la PWA en Nextjs pueda hacer fetch)
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	r.GET("/api/user/status", func(c *gin.Context) {
		var form Formulario

		// 1. Verificar si hay un formulario registrado
		if err := db.Order("fecha_registro desc").First(&form).Error; err != nil {
			// Si no hay, mandamos isRegistered: false y terminamos
			c.JSON(200, gin.H{"isRegistered": false})
			return
		}

		// 2. Traer los Macros calculados
		var macros Macro
		db.Where("id_formulario = ?", form.IDFormulario).First(&macros)

		// 3. Buscar el registro exacto del DÍA DE HOY
		hoyStr := time.Now().Format("2006-01-02")
		var track DailyTrack
		var logs []FoodLog // Arreglo para guardar el historial de comidas

		result := db.Where("date_track = ?", hoyStr).First(&track)

		if result.Error == nil {
			// Si el usuario ya comió algo hoy, buscamos todas las comidas asociadas a este track
			// (GORM devuelve un slice vacío automáticamente si no encuentra nada, no se cae)
			db.Where("id_dayli_track = ?", track.IDDayliTrack).Find(&logs)
		} else {
			// Si es un día nuevo y aún no registra nada, mandamos un track en ceros para el UI
			track = DailyTrack{
				CaloriesCount: 0,
				Protein:       0,
				Carbs:         0,
				Fat:           0,
				Fiber:         0,
				Water:         0,
			}
			logs = []FoodLog{} // Arreglo vacío
		}

		// 4. Devolver TODO el estado inicial limpio
		c.JSON(200, gin.H{
			"isRegistered": true,
			"macros":       macros,
			"dailyTrack":   track,
			"foodLogs":     logs,
		})
	})

	r.POST("/api/formulario", func(c *gin.Context) {
		var f Formulario
		if err := c.ShouldBindJSON(&f); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		f.FechaRegistro = time.Now()

		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&f).Error; err != nil {
				return err
			}
			macrosCalculados := calcularRequerimientos(f)
			macrosCalculados.IDFormulario = f.IDFormulario
			if err := tx.Create(&macrosCalculados).Error; err != nil {
				return err
			}
			return nil
		})

		if err != nil {
			c.JSON(500, gin.H{"error": "No se pudo procesar el formulario"})
			return
		}
		c.JSON(201, gin.H{"message": "Perfil y Macros calculados :v"})
	})

	r.POST("/api/track", func(c *gin.Context) {
		if isRateLimited() {
			log.Println("Peticion bloqueada por rate limit GLOBAL BLOCK")
			c.JSON(429, gin.H{"error": "Demasiadas solicitudes. Por favor espera 20 segundos."})
			return
		}
		description := c.PostForm("description")
		file, header, err := c.Request.FormFile("image")
		if err != nil {
			log.Println(" Error leyendo imagen:", err)
			c.JSON(400, gin.H{"error": "Se requiere una imagen válida"})
			return
		}
		defer file.Close()

		imgBytes, _ := io.ReadAll(file)
		mimeType := strings.TrimPrefix(header.Header.Get("Content-Type"), "image/")
		log.Println(" Imagen recibida:", mimeType, len(imgBytes), "bytes")

		ctx := context.Background()
		client, err := genai.NewClient(ctx, option.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
		if err != nil {
			log.Println(" Error creando cliente Gemini:", err)
			c.JSON(500, gin.H{"error": "Error conectando con Gemini"})
			return
		}
		defer client.Close()

		model := client.GenerativeModel("gemini-2.5-flash")

		promptText := "Eres un nutricionista. Analiza la imagen. Contexto extra: '" + description + "'. " +
			"Devuelve ÚNICAMENTE un objeto JSON válido. NO uses bloques de código markdown (como ```json). NO agregues texto antes ni después. " +
			"Usa exactamente estas llaves en minúscula con valores numéricos enteros: " +
			"{\"food\": \"Nombre del plato\", \"calories\": 0, \"protein\": 0, \"carbs\": 0, \"fat\": 0, \"fiber\": 0}"

		log.Println("⏳ Enviando a Gemini...")
		//resp, err := model.GenerateContent(ctx, genai.Text(promptText), genai.ImageData(mimeType, imgBytes))
		resp, err := model.GenerateContent(ctx, genai.Text(promptText))
		if err != nil {
			log.Println(" Error de Gemini API:", err)
			c.JSON(500, gin.H{"error": "Gemini falló al generar contenido", "details": err.Error()})
			return
		}

		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			log.Println(" Respuesta vacía de Gemini")
			c.JSON(500, gin.H{"error": "Respuesta vacía de la IA"})
			return
		}

		// Extracción y LIMPIEZA del JSON
		rawText := string(resp.Candidates[0].Content.Parts[0].(genai.Text))
		log.Println("rawText original:", rawText) // Ver qué devolvió la IA

		rawText = strings.TrimPrefix(strings.TrimSpace(rawText), "```json")
		rawText = strings.TrimPrefix(strings.TrimSpace(rawText), "```")
		rawText = strings.TrimSuffix(strings.TrimSpace(rawText), "```")
		rawText = strings.TrimSpace(rawText)

		var geminiData GeminiNutritionResponse
		if err := json.Unmarshal([]byte(rawText), &geminiData); err != nil {
			log.Println(" Error parseando JSON de Gemini:", err, "\nTexto limpio:", rawText)
			c.JSON(500, gin.H{"error": "Error parseando la respuesta de la IA"})
			return
		}
		log.Println(" JSON parseado con éxito:", geminiData)

		var activeMacro Macro
		// Buscamos el macro asegurándonos de manejar si no existe
		if err := db.Order("id_macro desc").First(&activeMacro).Error; err != nil {
			log.Println(" No se encontró un Macro activo:", err)
			c.JSON(400, gin.H{"error": "Primero debes llenar tu formulario de metas"})
			return
		}

		hoy := time.Now().Format("2006-01-02")
		var track DailyTrack
		result := db.Where("date_track = ?", hoy).First(&track)

		log.Println(" Guardando en BD...")
		errDB := db.Transaction(func(tx *gorm.DB) error {
			if result.Error != nil {
				track = DailyTrack{
					IDMacro:       activeMacro.IDMacro,
					CaloriesCount: geminiData.Calories,
					Protein:       geminiData.Protein,
					Carbs:         geminiData.Carbs,
					Fat:           geminiData.Fat,
					Fiber:         geminiData.Fiber,
					DateTrack:     time.Now(),
				}
				if err := tx.Create(&track).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Model(&track).Updates(map[string]interface{}{
					"calories_count": track.CaloriesCount + geminiData.Calories,
					"protein":        track.Protein + geminiData.Protein,
					"carbs":          track.Carbs + geminiData.Carbs,
					"fat":            track.Fat + geminiData.Fat,
					"fiber":          track.Fiber + geminiData.Fiber,
				}).Error; err != nil {
					return err
				}
			}

			foodLog := FoodLog{
				IDDayliTrack: track.IDDayliTrack,
				Food:         geminiData.Food,
				Calories:     geminiData.Calories,
				Protein:      geminiData.Protein,
				Carbs:        geminiData.Carbs,
				Fat:          geminiData.Fat,
				Fiber:        geminiData.Fiber,
			}
			if err := tx.Create(&foodLog).Error; err != nil {
				return err
			}
			return nil
		})

		if errDB != nil {
			log.Println("❌ Error en Transacción BD:", errDB)
			c.JSON(500, gin.H{"error": "Error al guardar en la base de datos"})
			return
		}

		log.Println(" Todo guardado correctamente")
		c.JSON(200, gin.H{
			"message": "Comida registrada :v",
			"foodLog": geminiData,
			"dia":     track,
		})
	})

	r.POST("/api/track/water", func(c *gin.Context) {
		var req WaterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Datos inválidos"})
			return
		}

		var activeMacro Macro
		if err := db.Order("id_macro desc").First(&activeMacro).Error; err != nil {
			c.JSON(400, gin.H{"error": "No hay meta activa"})
			return
		}

		hoy := time.Now().Format("2006-01-02")
		var track DailyTrack
		result := db.Where("date_track = ?", hoy).First(&track)

		if result.Error != nil {
			// Si no ha registrado comidas hoy, creamos el track solo con el agua
			nuevoAgua := req.Amount
			if nuevoAgua < 0 {
				nuevoAgua = 0
			} // Evitar negativos

			track = DailyTrack{
				IDMacro:   activeMacro.IDMacro,
				Water:     nuevoAgua,
				DateTrack: time.Now(),
			}
			db.Create(&track)
		} else {
			// Si ya existe, sumamos/restamos el agua
			nuevoAgua := track.Water + req.Amount
			if nuevoAgua < 0 {
				nuevoAgua = 0
			}

			db.Model(&track).Update("water", nuevoAgua)
			track.Water = nuevoAgua
		}

		c.JSON(200, gin.H{"message": "Agua actualizada", "dia": track})
	})

	log.Println(" Servidor corriendo en el puerto 8080")
	r.Run(":8080")
}
